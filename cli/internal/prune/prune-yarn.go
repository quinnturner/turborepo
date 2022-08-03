package prune

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"strings"

	"github.com/vercel/turborepo/cli/internal/context"
	"github.com/vercel/turborepo/cli/internal/fs"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

// Prune creates a smaller monorepo with only the required workspaces
func (p *prune) pruneYarn(opts *opts, outDir *fs.AbsolutePath, ctx *context.Context) error {
	packageJSONPath := outDir.Join("package.json")
	if err := packageJSONPath.EnsureDir(); err != nil {
		return errors.Wrap(err, "could not create output directory")
	}
	workspaces := []string{}
	lockfile := p.config.RootPackageJSON.YarnSubLockfile
	targets := []interface{}{opts.scope}
	internalDeps, err := ctx.TopologicalGraph.Ancestors(opts.scope)
	if err != nil {
		return errors.Wrap(err, "could find traverse the dependency graph to find topological dependencies")
	}
	targets = append(targets, internalDeps.List()...)

	for _, internalDep := range targets {
		if internalDep == ctx.RootNode {
			continue
		}
		workspaces = append(workspaces, ctx.PackageInfos[internalDep].Dir)
		if opts.docker {
			targetDir := outDir.Join("full", ctx.PackageInfos[internalDep].Dir)
			jsonDir := outDir.Join("json", ctx.PackageInfos[internalDep].PackageJSONPath)
			if err := targetDir.EnsureDir(); err != nil {
				return errors.Wrapf(err, "failed to create folder %v for %v", targetDir, internalDep)
			}
			if err := fs.RecursiveCopy(ctx.PackageInfos[internalDep].Dir, targetDir.ToStringDuringMigration()); err != nil {
				return errors.Wrapf(err, "failed to copy %v into %v", internalDep, targetDir)
			}
			if err := jsonDir.EnsureDir(); err != nil {
				return errors.Wrapf(err, "failed to create folder %v for %v", jsonDir, internalDep)
			}
			if err := fs.RecursiveCopy(ctx.PackageInfos[internalDep].PackageJSONPath, jsonDir.ToStringDuringMigration()); err != nil {
				return errors.Wrapf(err, "failed to copy %v into %v", internalDep, jsonDir)
			}
		} else {
			targetDir := outDir.Join(ctx.PackageInfos[internalDep].Dir)
			if err := targetDir.EnsureDir(); err != nil {
				return errors.Wrapf(err, "failed to create folder %v for %v", targetDir, internalDep)
			}
			if err := fs.RecursiveCopy(ctx.PackageInfos[internalDep].Dir, targetDir.ToStringDuringMigration()); err != nil {
				return errors.Wrapf(err, "failed to copy %v into %v", internalDep, targetDir)
			}
		}

		for k, v := range ctx.PackageInfos[internalDep].YarnSubLockfile {
			lockfile[k] = v
		}

		p.ui.Output(fmt.Sprintf(" - Added %v", ctx.PackageInfos[internalDep].Name))
	}
	p.logger.Trace("new workspaces", "value", workspaces)
	if opts.docker {
		if fs.FileExists(".gitignore") {
			if err := fs.CopyFile(&fs.LstatCachedFile{Path: p.config.Cwd.Join(".gitignore")}, outDir.Join("full", ".gitignore").ToStringDuringMigration()); err != nil {
				return errors.Wrap(err, "failed to copy root .gitignore")
			}
		}
		// We only need to actually copy turbo.json into "full" folder since it isn't needed for installation in docker
		if fs.FileExists("turbo.json") {
			if err := fs.CopyFile(&fs.LstatCachedFile{Path: p.config.Cwd.Join("turbo.json")}, outDir.Join("full", "turbo.json").ToStringDuringMigration()); err != nil {
				return errors.Wrap(err, "failed to copy root turbo.json")
			}
		}

		if err := fs.CopyFile(&fs.LstatCachedFile{Path: p.config.Cwd.Join("package.json")}, outDir.Join("full", "package.json").ToStringDuringMigration()); err != nil {
			return errors.Wrap(err, "failed to copy root package.json")
		}

		if err := fs.CopyFile(&fs.LstatCachedFile{Path: p.config.Cwd.Join("package.json")}, outDir.Join("json", "package.json").ToStringDuringMigration()); err != nil {
			return errors.Wrap(err, "failed to copy root package.json")
		}
	} else {
		if fs.FileExists(".gitignore") {
			if err := fs.CopyFile(&fs.LstatCachedFile{Path: p.config.Cwd.Join(".gitignore")}, outDir.Join(".gitignore").ToStringDuringMigration()); err != nil {
				return errors.Wrap(err, "failed to copy root .gitignore")
			}
		}

		if fs.FileExists("turbo.json") {
			if err := fs.CopyFile(&fs.LstatCachedFile{Path: p.config.Cwd.Join("turbo.json")}, outDir.Join("turbo.json").ToStringDuringMigration()); err != nil {
				return errors.Wrap(err, "failed to copy root turbo.json")
			}
		}

		if err := fs.CopyFile(&fs.LstatCachedFile{Path: p.config.Cwd.Join("package.json")}, outDir.Join("package.json").ToStringDuringMigration()); err != nil {
			return errors.Wrap(err, "failed to copy root package.json")
		}
	}

	var b bytes.Buffer
	yamlEncoder := yaml.NewEncoder(&b)
	yamlEncoder.SetIndent(2)
	if err := yamlEncoder.Encode(lockfile); err != nil {
		return errors.Wrap(err, "failed to materialize sub-lockfile. This can happen if your lockfile contains merge conflicts or is somehow corrupted. Please report this if it occurs")
	}
	if err := outDir.Join("yarn.lock").WriteFile(b.Bytes(), fs.DirPermissions); err != nil {
		return errors.Wrap(err, "failed to write sub-lockfile")
	}

	yarnTmpFilePath := outDir.Join("yarn-tmp.lock")
	tmpGeneratedLockfile, err := yarnTmpFilePath.Create()
	if err != nil {
		return errors.Wrap(err, "failed create temporary lockfile")
	}
	tmpGeneratedLockfileWriter := bufio.NewWriter(tmpGeneratedLockfile)

	tmpGeneratedLockfileWriter.WriteString("# THIS IS AN AUTOGENERATED FILE. DO NOT EDIT THIS FILE DIRECTLY.\n# yarn lockfile v1\n\n")

	// because of yarn being yarn, we need to inject lines in between each block of YAML to make it "valid" SYML
	lockFilePath := outDir.Join("yarn.lock")
	generatedLockfile, err := lockFilePath.Open()
	if err != nil {
		return errors.Wrap(err, "failed to massage lockfile")
	}

	scan := bufio.NewScanner(generatedLockfile)
	buf := make([]byte, 0, 1024*1024)
	scan.Buffer(buf, 10*1024*1024)
	for scan.Scan() {
		line := scan.Text() //Writing to Stdout
		if !strings.HasPrefix(line, " ") {
			tmpGeneratedLockfileWriter.WriteString(fmt.Sprintf("\n%v\n", strings.ReplaceAll(line, "'", "\"")))
		} else {
			tmpGeneratedLockfileWriter.WriteString(fmt.Sprintf("%v\n", strings.ReplaceAll(line, "'", "\"")))
		}
	}
	// Make sure to flush the log write before we start saving it.
	if err := tmpGeneratedLockfileWriter.Flush(); err != nil {
		return errors.Wrap(err, "failed to flush to temporary lock file")
	}

	// Close the files before we rename them
	if err := tmpGeneratedLockfile.Close(); err != nil {
		return errors.Wrap(err, "failed to close temporary lock file")
	}
	if err := generatedLockfile.Close(); err != nil {
		return errors.Wrap(err, "failed to close existing lock file")
	}

	// Rename the file
	if err := os.Rename(yarnTmpFilePath.ToStringDuringMigration(), lockFilePath.ToStringDuringMigration()); err != nil {
		return errors.Wrap(err, "failed finalize lockfile")
	}
	return nil
}