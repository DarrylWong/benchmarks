// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package harnesses

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"golang.org/x/benchmarks/sweet/common"
	"golang.org/x/benchmarks/sweet/common/log"
)

// CockroachDB implements the Harness interface.
type CockroachDB struct{}

func (h CockroachDB) CheckPrerequisites() error {
	// Cockroachdb is only supported on arm64 and amd64 architectures.
	if runtime.GOARCH != "arm64" && runtime.GOARCH != "amd64" {
		return fmt.Errorf("requires amd64 or arm64")
	}
	return nil
}

func (h CockroachDB) Get(gcfg *common.GetConfig) error {
	// Build against the latest stable release.
	// Deep clone the repo as we need certain submodules, i.e.
	// PROJ, for the build to work.
	return gitDeepClone(
		gcfg.SrcDir,
		"https://github.com/cockroachdb/cockroach",
		"v24.1.0-rc.1",
	)
}

func (h CockroachDB) Build(cfg *common.Config, bcfg *common.BuildConfig) error {
	// Build the cockroach binary.
	// We do this by using the cockroach `dev` tool. The dev tool is a bazel
	// wrapper normally used for building cockroach, but can also be used to
	// generate artifacts that can then be built by `go build`.

	// Install bazel via bazelisk which is used by `dev`.
	if err := cfg.GoTool().Do("", "install", "github.com/bazelbuild/bazelisk@latest"); err != nil {
		return fmt.Errorf("error building bazelisk: %v", err)
	}

	// Clean up the bazel workspace. If we don't do this, our _bazel directory
	// will quickly grow as Bazel treats each run as its own workspace with its
	// own artifacts.
	defer func() {
		cmd := exec.Command("bazel", "clean", "--expunge")
		cmd.Dir = bcfg.SrcDir
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		// Cleanup is best effort, there might not be anything to clean up
		// if we fail early enough in the build process.
		_ = cmd.Run()
	}()

	// Configure the build env.
	env := cfg.BuildEnv.Env
	env = env.Prefix("PATH", filepath.Join(cfg.GoRoot, "bin")+":")
	env = env.MustSet("GOROOT=" + cfg.GoRoot)

	// Use bazel to generate the artifacts needed to enable a `go build`.
	cmd := exec.Command("bazel", "run", "//pkg/gen:code")
	cmd.Dir = bcfg.SrcDir
	cmd.Env = env.Collapse()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	// Build the c-deps needed.
	cmd = exec.Command("bazel", "run", "//pkg/cmd/generate-cgo:generate-cgo", "--run_under", fmt.Sprintf("cd %s && ", bcfg.SrcDir))
	cmd.Dir = bcfg.SrcDir
	cmd.Env = env.Collapse()
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return err
	}

	// Finally build the cockroach binary with `go build`. Build the
	// cockroach-short binary as it is functionally the same, but
	// without the UI, making it much quicker to build.
	if err := cfg.GoTool().BuildPath(filepath.Join(bcfg.SrcDir, "pkg/cmd/cockroach-short"), bcfg.BinDir); err != nil {
		return err
	}

	// Rename the binary from cockroach-short to cockroach for
	// ease of use.
	if err := copyFile(filepath.Join(bcfg.BinDir, "cockroach"), filepath.Join(bcfg.BinDir, "cockroach-short")); err != nil {
		return err
	}

	// Build the benchmark wrapper.
	if err := cfg.GoTool().BuildPath(bcfg.BenchDir, filepath.Join(bcfg.BinDir, "cockroachdb-bench")); err != nil {
		return err
	}

	cmd = exec.Command("chmod", "-R", "755", filepath.Join(bcfg.BinDir, "cockroachdb-bench"))
	if err := cmd.Run(); err != nil {
		return err
	}
	return nil
}

func (h CockroachDB) Run(cfg *common.Config, rcfg *common.RunConfig) error {
	for _, bench := range []string{"kv0/nodes=1", "kv50/nodes=1", "kv95/nodes=1", "kv0/nodes=3", "kv50/nodes=3", "kv95/nodes=3"} {
		args := append(rcfg.Args, []string{
			"-bench", bench,
			"-cockroachdb-bin", filepath.Join(rcfg.BinDir, "cockroach"),
			"-tmp", rcfg.TmpDir,
		}...)
		if rcfg.Short {
			args = append(args, "-short")
		}
		cmd := exec.Command(
			filepath.Join(rcfg.BinDir, "cockroachdb-bench"),
			args...,
		)
		cmd.Env = cfg.ExecEnv.Collapse()
		cmd.Stdout = rcfg.Results
		cmd.Stderr = rcfg.Results
		log.TraceCommand(cmd, false)
		if err := cmd.Run(); err != nil {
			return err
		}
		// Delete tmp because cockroachdb will have written something there and
		// might attempt to reuse it. We don't want to reuse the same cluster.
		if err := rmDirContents(rcfg.TmpDir); err != nil {
			return err
		}
	}
	return nil
}
