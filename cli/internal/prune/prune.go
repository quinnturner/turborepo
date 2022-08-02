package prune

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/vercel/turborepo/cli/internal/cache"
	"github.com/vercel/turborepo/cli/internal/config"
	"github.com/vercel/turborepo/cli/internal/context"
	"github.com/vercel/turborepo/cli/internal/fs"
	"github.com/vercel/turborepo/cli/internal/util"

	"github.com/fatih/color"
	"github.com/hashicorp/go-hclog"
	"github.com/mitchellh/cli"
	"github.com/pkg/errors"
)

// PruneCommand is a Command implementation that tells Turbo to run a task
type PruneCommand struct {
	Config *config.Config
	Ui     *cli.ColoredUi
}

// Synopsis of run command
func (c *PruneCommand) Synopsis() string {
	return getCmd(c.Config, c.Ui).Short
}

// Help returns information about the `run` command
func (c *PruneCommand) Help() string {
	cmd := getCmd(c.Config, c.Ui)
	return util.HelpForCobraCmd(cmd)
}

// Run implements cli.Command.Run
func (c *PruneCommand) Run(args []string) int {
	cmd := getCmd(c.Config, c.Ui)
	cmd.SetArgs(args)
	if err := cmd.Execute(); err != nil {
		return 1
	}
	return 0
}

type opts struct {
	scope     string
	docker    bool
	outputDir string
}

func addPruneFlags(opts *opts, flags *pflag.FlagSet) {
	flags.StringVar(&opts.scope, "scope", "", "Specify package to act as entry point for pruned monorepo (required).")
	flags.BoolVar(&opts.docker, "docker", false, "Output pruned workspace into 'full' and 'json' directories optimized for Docker layer caching.")
	flags.StringVar(&opts.outputDir, "out-dir", "out", "Set the root directory for files output by this command")
	// No-op the cwd flag while the root level command is not yet cobra
	_ = flags.String("cwd", "", "")
	if err := flags.MarkHidden("cwd"); err != nil {
		// Fail fast if we have misconfigured our flags
		panic(err)
	}
}

func getCmd(config *config.Config, ui cli.Ui) *cobra.Command {
	opts := &opts{}
	cmd := &cobra.Command{
		Use:                   "turbo prune --scope=<package name> [<flags>]",
		Short:                 "Prepare a subset of your monorepo.",
		SilenceUsage:          true,
		SilenceErrors:         true,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			logger := config.Logger.Named("prune")
			if len(args) > 0 {
				err := errors.Errorf("unexpected arguments: %v", args)
				logError(logger, ui, err)
				return err
			}
			if opts.scope == "" {
				err := errors.New("at least one target must be specified")
				logError(logger, ui, err)
				return err
			}
			p := &prune{
				logger: logger,
				ui:     ui,
				config: config,
			}
			if err := p.prune(opts); err != nil {
				logError(p.logger, p.ui, err)
				return err
			}
			return nil
		},
	}
	addPruneFlags(opts, cmd.Flags())
	return cmd
}

func logError(logger hclog.Logger, ui cli.Ui, err error) {
	logger.Error("error", err)
	pref := color.New(color.Bold, color.FgRed, color.ReverseVideo).Sprint(" ERROR ")
	ui.Error(fmt.Sprintf("%s%s", pref, color.RedString(" %v", err)))
}

type prune struct {
	logger hclog.Logger
	ui     cli.Ui
	config *config.Config
}

// Prune creates a smaller monorepo with only the required workspaces
func (p *prune) prune(opts *opts) error {
	cacheDir := cache.DefaultLocation(p.config.Cwd)
	turboJSON, err := fs.ReadTurboConfig(p.config.Cwd, p.config.RootPackageJSON)
	if err != nil {
		return err
	}
	ctx, err := context.New(context.WithGraph(p.config, turboJSON, cacheDir))
	if err != nil {
		return errors.Wrap(err, "could not construct graph")
	}
	p.logger.Trace("scope", "value", opts.scope)
	target, scopeIsValid := ctx.PackageInfos[opts.scope]
	if !scopeIsValid {
		return errors.Errorf("invalid scope: package %v not found", opts.scope)
	}
	outDir := p.config.Cwd.Join(opts.outputDir)
	p.logger.Trace("target", "value", target.Name)
	p.logger.Trace("directory", "value", target.Dir)
	p.logger.Trace("external deps", "value", target.UnresolvedExternalDeps)
	p.logger.Trace("internal deps", "value", target.InternalDeps)
	p.logger.Trace("docker", "value", opts.docker)
	p.logger.Trace("out dir", "value", outDir.ToString())

	if util.IsYarnClassic(ctx.PackageManager.Name) {
		return p.pruneYarn(opts, &outDir, ctx)
	}
	if util.IsYarnBerry(ctx.PackageManager.Name) {
		return p.pruneBerry(opts, &outDir, ctx)
	}
	return errors.Errorf("this command is not yet implemented for %s", ctx.PackageManager.Name)
}
