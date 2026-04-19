package ort

import (
	"errors"
	"fmt"
	"io"

	"gravel/ort/diff3"

	"github.com/go-git/go-git/v6"
	"github.com/go-git/go-git/v6/plumbing"
	"github.com/go-git/go-git/v6/plumbing/format/index"
	"github.com/go-git/go-git/v6/plumbing/object"
	"github.com/go-git/go-git/v6/plumbing/storer"
	"github.com/go-git/go-git/v6/utils/merkletrie"
)

const (
	FastForwardMerge git.MergeStrategy = iota
	FastForwardOnly
	OrtMerge
)

const (
	MERGE_HEAD plumbing.ReferenceName = "MERGE_HEAD"
)

var (
	ErrUnrelatedHistories = errors.New("no common ancestor: unrelated histories")
	ErrMergeConflict      = errors.New("merge conflict")
)

type MergeOptions struct {
	Strategy               git.MergeStrategy
	OrtMergeStrategyOption git.OrtMergeStrategyOption
	Progress               io.Writer
}

func Merge(r *git.Repository, ref plumbing.Reference, opts MergeOptions) error {
	// Check strategy before moving HEAD
	if opts.Strategy != OrtMerge &&
		opts.Strategy != FastForwardMerge &&
		opts.Strategy != FastForwardOnly {
		return git.ErrUnsupportedMergeStrategy
	}

	head, err := r.Head()
	if err != nil {
		return err
	}

	theirCommit, err := r.CommitObject(ref.Hash())
	if err != nil {
		return err
	}

	ourCommit, err := r.CommitObject(head.Hash())
	if err != nil {
		return err
	}

	// Ignore error as not having a shallow list is optional here.
	shallowList, _ := r.Storer.Shallow()
	var earliestShallow *plumbing.Hash
	if len(shallowList) > 0 {
		earliestShallow = &shallowList[0]
	}

	ff, err := isFastForward(r.Storer, head.Hash(), ref.Hash(), earliestShallow)
	if err != nil {
		return err
	}

	var patch *object.Patch
	// All strategies allow FF unless explicitly disabled
	if ff {
		patch, err = ourCommit.Patch(theirCommit)
		if err != nil {
			return err
		}

		if opts.Progress != nil {
			_, _ = fmt.Fprintf(opts.Progress,
				"Updating %s...%s\nFast-forward\n%s",
				head.Hash().String()[:7],
				ref.Hash().String()[:7],
				patch.Stats())
		}
		return r.Storer.SetReference(plumbing.NewHashReference(head.Name(), ref.Hash()))
	}

	if opts.Strategy == FastForwardOnly {
		return git.ErrFastForwardMergeNotPossible
	}

	// Find common bases to merge from
	baseCommits, err := ourCommit.MergeBase(theirCommit)
	if err != nil {
		return err
	}

	if len(baseCommits) < 1 {
		return ErrUnrelatedHistories
	}
	// TODO: recursive merging

	baseTree, err := baseCommits[0].Tree()
	if err != nil {
		return err
	}

	ourTree, err := ourCommit.Tree()
	if err != nil {
		return err
	}

	theirTree, err := theirCommit.Tree()
	if err != nil {
		return err
	}

	baseToOur, err := baseTree.Diff(ourTree)
	if err != nil {
		return err
	}

	baseToTheir, err := baseTree.Diff(theirTree)
	if err != nil {
		return err
	}

	// Prepare changes per files using filename as keys
	changes := make(map[string]struct {
		ours   *object.Change
		theirs *object.Change
	})

	for _, change := range baseToOur {
		path := change.To.Name
		// If it was deleted find its name using .From
		if path == "" {
			path = change.From.Name
		}
		pair := changes[path]
		pair.ours = change
		changes[path] = pair
	}

	for _, change := range baseToTheir {
		path := change.To.Name
		if path == "" {
			path = change.From.Name
		}
		pair := changes[path]
		pair.theirs = change
		changes[path] = pair
	}

	w, err := r.Worktree()
	if err != nil {
		return err
	}

	mergeHasConflict := false

	for filepath, pair := range changes {
		var baseFile, ourFile, theirFile *object.File
		var baseReader, ourReader, theirReader io.ReadCloser

		switch {
		// If only our file has changed
		case pair.ours != nil && pair.theirs == nil:
			action, err := pair.ours.Action()
			if err != nil {
				return err
			}

			switch action {
			// Our file was created or modified
			case merkletrie.Insert, merkletrie.Modify:
				_, ourFile, err = pair.ours.Files()
				if err != nil {
					return err
				}

				ourReader, err = ourFile.Reader()
				if err != nil {
					return err
				}

				var dst io.WriteCloser
				dst, err = w.Filesystem.Create(filepath)
				if err != nil {
					return err
				}
				defer func() { _ = dst.Close() }()

				if _, err = io.Copy(dst, ourReader); err != nil {
					return err
				}

				if _, err = w.Add(filepath); err != nil {
					return err
				}

			// Our file was deleted
			case merkletrie.Delete:
				// if err = w.Filesystem.Remove(filepath); err != nil && !os.IsNotExist(err) {
				// 	return err
				// }

				// Remove file from index and filesystem, noop if already deleted
				if _, err = w.Remove(filepath); err != nil && !errors.Is(err, index.ErrEntryNotFound) {
					return err
				}
			}

		case pair.ours == nil && pair.theirs != nil:
			action, err := pair.theirs.Action()
			if err != nil {
				return err
			}

			switch action {
			// Their file was created or inserted
			case merkletrie.Insert, merkletrie.Modify:
				_, theirFile, err = pair.theirs.Files()
				if err != nil {
					return err
				}

				theirReader, err = theirFile.Reader()
				if err != nil {
					return err
				}

				var dst io.WriteCloser
				dst, err := w.Filesystem.Create(filepath)
				if err != nil {
					return err
				}
				defer func() { _ = dst.Close() }()

				if _, err = io.Copy(dst, theirReader); err != nil {
					return err
				}

				if _, err = w.Add(filepath); err != nil {
					return err
				}

			// Their file has been deleted
			case merkletrie.Delete:
				// if err = w.Filesystem.Remove(filepath); err != nil && !os.IsNotExist(err) {
				// 	return err
				// }

				if _, err = w.Remove(filepath); err != nil && !errors.Is(err, index.ErrEntryNotFound) {
					return err
				}
			}

		// If both file changed Three Way Merging is needed
		// Note: Maybe use the "default" keyword
		case pair.ours != nil && pair.theirs != nil:

			baseFile, ourFile, err = pair.ours.Files()
			if err != nil {
				return err
			}

			// Ignore second base as it should the same
			_, theirFile, err = pair.theirs.Files()
			if err != nil {
				return err
			}

			var ourAction, theirAction merkletrie.Action
			ourAction, err = pair.ours.Action()
			if err != nil {
				return err
			}

			theirAction, err = pair.theirs.Action()
			if err != nil {
				return err
			}

			switch {
			// Added or Modified by both
			case ourAction == merkletrie.Modify && theirAction == merkletrie.Modify,
				ourAction == merkletrie.Insert && theirAction == merkletrie.Insert:

				// If they made the same changes
				if ourFile.Hash == theirFile.Hash {
					if _, err = w.Add(filepath); err != nil {
						return err
					}
					continue // Skip
				}

				baseReader, err = baseFile.Reader()
				if err != nil {
					return err
				}
				defer func() { _ = baseReader.Close() }()

				ourReader, err = ourFile.Reader()
				if err != nil {
					return err
				}
				defer func() { _ = ourReader.Close() }()

				_, theirFile, err = pair.theirs.Files()
				if err != nil {
					return err
				}

				theirReader, err = theirFile.Reader()
				if err != nil {
					return err
				}
				defer func() { _ = theirReader.Close() }()

				mergeResult, err := diff3.Merge(
					ourReader,
					baseReader,
					theirReader,
					true,
					head.Name().Short(),
					ref.Name().Short(),
				)
				if err != nil {
					return err
				}

				file, err := w.Filesystem.Create(filepath)
				if err != nil {
					return err
				}
				defer func() { _ = file.Close() }()

				if _, err = io.Copy(file, mergeResult.Result); err != nil {
					return err
				}

				mergeHasConflict = mergeHasConflict || mergeResult.Conflicts

				if !mergeResult.Conflicts {
					if _, err = w.Add(filepath); err != nil {
						return err
					}
				}

			// Deleted by both
			case ourAction == merkletrie.Delete && theirAction == merkletrie.Delete:
				// if err = w.Filesystem.Remove(filepath); err != nil && !os.IsNotExist(err) {
				// 	return err
				// }
				if _, err = w.Remove(
					filepath,
				); err != nil &&
					!errors.Is(err, index.ErrEntryNotFound) {
					return err
				}

				// Inserted / Modified by us, deleted by them
			case (ourAction == merkletrie.Insert || ourAction == merkletrie.Modify) && theirAction == merkletrie.Delete:
				var dst io.Writer
				dst, err = w.Filesystem.Create(filepath)
				if err != nil {
					return err
				}

				ourReader, err = ourFile.Reader()
				if err != nil {
					return err
				}

				if _, err = io.Copy(dst, ourReader); err != nil {
					return err
				}

				if _, err = w.Add(filepath); err != nil {
					return err
				}
				// TODO: mark in index

			// Inserted / Modified by them, deleted by us
			case (theirAction == merkletrie.Insert || theirAction == merkletrie.Modify) && ourAction == merkletrie.Delete:
				dstFile, err := w.Filesystem.Create(filepath)
				if err != nil {
					return err
				}
				theirReader, err = theirFile.Reader()
				if err != nil {
					return err
				}
				if _, err = io.Copy(dstFile, theirReader); err != nil {
					return err
				}
				if _, err = w.Add(filepath); err != nil {
					return err
				}
				// TODO: mark in index
			}
		}
	}

	if mergeHasConflict {
		err = r.Storer.SetReference(plumbing.NewHashReference(MERGE_HEAD, ref.Hash()))
		if err != nil {
			return err
		}
		return ErrMergeConflict
	}

	status, err := w.Status()
	if err != nil {
		return err
	}

	if status.IsClean() {
		return nil
	}

	var newHash plumbing.Hash
	newHash, err = w.Commit(
		fmt.Sprintf(
			"Merge %s with %s",
			plumbing.NewBranchReferenceName(head.Name().Short()),
			ref.Name(),
		),
		&git.CommitOptions{
			Author:    &ourCommit.Author,
			Committer: &ourCommit.Committer,
			Parents:   []plumbing.Hash{ourCommit.Hash, theirCommit.Hash},
		},
	)
	if err != nil {
		return err
	}

	var newCommit *object.Commit
	newCommit, err = r.CommitObject(newHash)
	if err != nil {
		return err
	}

	patch, err = ourCommit.Patch(newCommit)
	if err != nil {
		return err
	}

	if opts.Progress != nil {
		_, _ = fmt.Fprintf(opts.Progress, "Merge made by the 'ort' strategy.\n%s", patch.Stats())
	}

	return err
}

func isFastForward(s storer.EncodedObjectStorer, old, newHash plumbing.Hash, earliestShallow *plumbing.Hash) (bool, error) {
	c, err := object.GetCommit(s, newHash)
	if err != nil {
		return false, err
	}

	parentsToIgnore := []plumbing.Hash{}
	if earliestShallow != nil {
		earliestCommit, err := object.GetCommit(s, *earliestShallow)
		if err != nil {
			return false, err
		}

		parentsToIgnore = earliestCommit.ParentHashes
	}

	found := false
	// stop iterating at the earliest shallow commit, ignoring its parents
	// note: when pull depth is smaller than the number of new changes on the remote, this fails due to missing parents.
	//       as far as i can tell, without the commits in-between the shallow pull and the earliest shallow, there's no
	//       real way of telling whether it will be a fast-forward merge.
	iter := object.NewCommitPreorderIter(c, nil, parentsToIgnore)
	err = iter.ForEach(func(c *object.Commit) error {
		if c.Hash != old {
			return nil
		}

		found = true
		return storer.ErrStop
	})
	return found, err
}
