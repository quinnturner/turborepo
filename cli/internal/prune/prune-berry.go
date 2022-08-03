package prune

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/vercel/turborepo/cli/internal/context"
	"github.com/vercel/turborepo/cli/internal/fs"
	"github.com/vercel/turborepo/cli/internal/util"

	"github.com/pkg/errors"
	"gopkg.in/yaml.v3"
)

type deduplicatedLockfileEntry struct {
	keys  []string
	entry *fs.BerryLockfileEntry
}

const resolutionQuoteIdx = len("  resolution: \"") - 1

// Prune creates a smaller monorepo with only the required workspaces
func (p *prune) pruneBerry(opts *opts, outDir *fs.AbsolutePath, ctx *context.Context) error {
	if isNMLinker, err := util.IsNMLinker(p.config.Cwd.ToStringDuringMigration()); err != nil {
		return errors.Wrap(err, "could not determine if yarn is using `nodeLinker: node-modules`")
	} else if !isNMLinker {
		return errors.New("only yarn v2/v3 with `nodeLinker: node-modules` is supported at this time")
	}
	packageJSONPath := outDir.Join("package.json")
	if err := packageJSONPath.EnsureDir(); err != nil {
		return errors.Wrap(err, "could not create output directory")
	}
	workspaces := []string{}
	lockfile := p.config.RootPackageJSON.BerrySubLockfile
	metadata := (*ctx.BerryLockfile)["__metadata"]
	lockfileVersion := metadata.Version
	lockfileCacheKey := metadata.CacheKey

	targets := []interface{}{opts.scope}
	internalDeps, err := ctx.TopologicalGraph.Ancestors(opts.scope)
	if err != nil {
		return errors.Wrap(err, "could find traverse the dependency graph to find topological dependencies")
	}
	targets = append(targets, internalDeps.List()...)

	deduplicatingLockfile := map[string]deduplicatedLockfileEntry{}

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

		for k, v := range ctx.PackageInfos[internalDep].BerrySubLockfile {
			resolvedVersion := k[0:strings.LastIndex(k, "@")] + "@" + v.Version
			if lockfileEntry, ok := deduplicatingLockfile[resolvedVersion]; !ok {
				deduplicatingLockfile[resolvedVersion] = deduplicatedLockfileEntry{
					entry: v,
					keys:  []string{k},
				}
			} else {
				found := false
				for _, entry := range lockfileEntry.keys {
					if entry == k {
						found = true
						break
					}
				}
				if !found {
					lockfileEntry.keys = append(lockfileEntry.keys, k)
					deduplicatingLockfile[resolvedVersion] = lockfileEntry
				}
			}
		}

		p.ui.Output(fmt.Sprintf(" - Added %v", ctx.PackageInfos[internalDep].Name))
	}

	for _, v := range deduplicatingLockfile {
		sort.Strings(v.keys)
		lockfilePackageVersion := strings.Join(v.keys, ", ")
		lockfile[lockfilePackageVersion] = v.entry
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

	tmpGeneratedLockfileWriter.WriteString(fmt.Sprintf("# This file is generated by running \"yarn install\" inside your project.\n# Manual changes might be lost - proceed with caution!\n\n__metadata:\n  version: %s\n  cacheKey: %s\n", lockfileVersion, lockfileCacheKey))

	// because of yarn being yarn, we need to inject lines in between each block of YAML to make it "valid" SYML
	lockFilePath := outDir.Join("yarn.lock")
	generatedLockfile, err := lockFilePath.Open()
	if err != nil {
		return errors.Wrap(err, "failed to massage lockfile")
	}

	scan := bufio.NewScanner(generatedLockfile)
	buf := make([]byte, 0, 2*1024*1024)
	scan.Buffer(buf, 10*1024*1024)

	// { "in-dependencies", "other", "after-dependencies" }
	scanState := "other"

	for scan.Scan() {
		line := scan.Text() //Writing to Stdout
		// Complex keys may start with `? `, remove them.
		if strings.HasPrefix(line, "? ") {
			line = line[2:]
		} else if strings.HasPrefix(line, ":") {
			line = " " + line[1:]
		}
		if !strings.HasPrefix(line, " ") {
			if !strings.HasSuffix(line, ":") {
				line = line + ":"
			}
			line = strings.ReplaceAll(line, "'", "\"")
			if !strings.HasPrefix(line, "\"") {
				line = "\"" + line[:len(line)-1] + "\":"
			}
			tmpGeneratedLockfileWriter.WriteString(fmt.Sprintf("\n%v\n", line))
		} else {
			// TODO: more performant string manipulation
			if strings.HasPrefix(line, "  resolution") {
				// quote the resolution if not already quoted
				if line[resolutionQuoteIdx] == '\'' {
					line = strings.ReplaceAll(line, "'", "\"")
				} else {
					line = line[:resolutionQuoteIdx] + "\"" + line[resolutionQuoteIdx:] + "\""
				}
			} else if strings.HasPrefix(line, "  dependencies") {
				scanState = "in-dependencies"
			} else if scanState == "in-dependencies" {
				if !strings.HasPrefix(line, "    ") {
					scanState = "after-dependencies"
				} else {
					// have to correct quotes for versions
					indexOfColon := strings.Index(line, ": ")
					dependencyVersion := line[indexOfColon+2:]
					if strings.HasPrefix(dependencyVersion, "\"") {
						// have to determine if this quote is appropriate
						// `debug: "4"` should be `debug: 4`
						if !strings.Contains(dependencyVersion, ">") && !strings.Contains(dependencyVersion, "<") {
							dependencyVersion = dependencyVersion[1 : len(dependencyVersion)-1]
							line = line[:indexOfColon+2] + dependencyVersion
						}
					}
				}
			}
			newLine := fmt.Sprintf("%v\n", strings.ReplaceAll(line, "'", "\""))
			tmpGeneratedLockfileWriter.WriteString(newLine)
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
