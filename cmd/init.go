package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"

	"gravel/components"
	"gravel/manifest"
	"gravel/ort"
	"gravel/source"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/go-git/go-billy/v6/memfs"
	"github.com/go-git/go-billy/v6/osfs"
	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/config"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/cache"
	"github.com/go-git/go-git/v6/storage"
	"github.com/go-git/go-git/v6/storage/filesystem"
	"github.com/go-git/go-git/v6/storage/memory"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// initCmd represents the init command
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a gravel App",
	Long:  `Starts the cli process`,

	RunE: RunE,

	SilenceUsage: true,
}

const (
	ManifestFlag = "manifest"
	Manifest     = "https://raw.githubusercontent.com/gravel-dev-1/cli/refs/heads/master/manifest.yaml"

	DryRunFlag = "dry-run"
	DryRun     = false

	VerboseFlag = "verbose"
	Verbose     = false
)

func init() {
	rootCmd.AddCommand(initCmd)
	initCmd.Flags().StringP(ManifestFlag, string(ManifestFlag[0]), Manifest, "sets the manifest")
	initCmd.Flags().
		Bool(DryRunFlag, DryRun, "perform a trial run with no changes made to filesystem")
	initCmd.Flags().Bool(VerboseFlag, Verbose, "runs in verbose mode")
}

func RunE(cmd *cobra.Command, args []string) error {
	flags := cmd.Flags()

	manifestFlag, err := flags.GetString(ManifestFlag)
	if err != nil {
		return err
	}

	reader, err := source.Resolve(manifestFlag)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()

	decodedManifest := new(manifest.Manifest)

	err = yaml.NewDecoder(reader).Decode(decodedManifest)
	if err != nil {
		return err
	}

	err = decodedManifest.Validate()
	if err != nil {
		return err
	}

	var dryRun bool
	dryRun, err = flags.GetBool(DryRunFlag)
	if err != nil {
		return err
	}

	var storer storage.Storer = memory.NewStorage()
	worktree := memfs.New()

	if !dryRun {
		// Get current working directory
		dir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current directory: %w", err)
		}

		// Determine the target directory (use first arg if provided, else current dir)
		targetDir := dir
		if len(args) > 0 && args[0] != "" {
			targetDir = args[0]
		}

		worktree = osfs.New(targetDir)
		dot, _ := worktree.Chroot(git.GitDirName)
		storer = filesystem.NewStorage(dot, cache.NewObjectLRUDefault())
	}

	var repo *git.Repository
	repo, err = git.Init(storer, git.WithWorkTree(worktree))
	if err != nil {
		return err
	}

	stdin := cmd.InOrStdin()

	stdout := cmd.OutOrStdout()

	var verbose bool
	verbose, err = flags.GetBool(VerboseFlag)
	if err != nil {
		return err
	}
	progress := io.Discard
	if verbose {
		progress = stdout
	}

	baseSelector := components.NewBaseSelector(decodedManifest.Base...)
	program := tea.NewProgram(
		baseSelector,
		tea.WithInput(stdin),
		tea.WithOutput(stdout),
		tea.WithContext(cmd.Context()),
	)
	if _, err = program.Run(); err != nil {
		return err
	}

	base := baseSelector.Selected()
	if base == nil {
		return nil
	}

	var origin *git.Remote
	origin, err = repo.CreateRemote(&config.RemoteConfig{
		Name: "origin",
		URLs: []string{base.Remote.URL},
	})
	if err != nil {
		return err
	}

	err = repo.Fetch(&git.FetchOptions{
		RemoteName: origin.Config().Name,
		Progress:   progress,
	})
	if err != nil {
		return err
	}

	var wt *git.Worktree
	wt, err = repo.Worktree()
	if err != nil {
		return err
	}

	// Get the remote reference
	ref, err := repo.Reference(plumbing.NewRemoteReferenceName("origin", base.Remote.Ref), true)
	if err != nil {
		return err
	}

	err = wt.Checkout(&git.CheckoutOptions{Branch: ref.Name()})
	if err != nil {
		return err
	}

	pluginSelector := components.NewBaseMultiSelector(decodedManifest.Plugins...)
	program = tea.NewProgram(
		pluginSelector,
		tea.WithInput(stdin),
		tea.WithOutput(stdout),
		tea.WithContext(cmd.Context()),
	)

	if _, err = program.Run(); err != nil {
		return err
	}

	selectedPlugins := pluginSelector.Selected()
	for index, plugin := range selectedPlugins {
		if plugin.Remote.Name == "" {
			plugin.Remote.Name = fmt.Sprintf("plugin-%d", index)
		}

		remote, err := repo.CreateRemote(&config.RemoteConfig{
			Name: plugin.Remote.Name,
			URLs: []string{plugin.Remote.URL},
		})
		if err != nil {
			return err
		}

		// Fetch the remote
		err = remote.Fetch(&git.FetchOptions{
			RemoteName: plugin.Remote.Name,
			Progress:   progress,
		})
		if err != nil && !errors.Is(err, git.NoErrAlreadyUpToDate) {
			return err
		}

		var pluginRef *plumbing.Reference
		pluginRef, err = repo.Reference(
			plumbing.NewRemoteReferenceName(remote.Config().Name, plugin.Remote.Ref),
			true,
		)
		if err != nil {
			return err
		}

		// err = repo.Merge(*pluginRef, git.MergeOptions{}) // WIP
		err = ort.Merge(repo, *pluginRef, ort.MergeOptions{
			Progress: progress,
		})
		if err != nil {
			return err
		}
	}

	return err
	// return wt.Reset(&git.ResetOptions{Mode: git.SoftReset})
}
