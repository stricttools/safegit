package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/stricttools/safegit/internal/commit"
	"github.com/stricttools/safegit/internal/exitcode"
	"github.com/stricttools/safegit/internal/git"
	"github.com/stricttools/strictcli/go/strictcli"
	"github.com/stricttools/strictspec/go/lifecycle"
	"github.com/stricttools/strictspec/go/lifecycle/index"
)

// This file is the commit command's half of the confidential-name rules.
//
// A repository is confidential while one of its releasables has a proprietary
// license period in effect, as its lifecycle-and-license record states
// (.strictmetadata/lifecycle-and-license/lifecycle-and-license.toml); it is
// public otherwise, and a repository without a record is public. safegit
// decides it from the record on disk, offline, and never asks GitHub.
//
// Every commit keeps the machine-local confidential-name index
// (<os.UserConfigDir()>/strictspec/confidential-names.toml) current for the
// repository it commits in: a confidential repository's entry is upserted with
// the names it protects, and a public repository's entry, if it has one, is
// removed. Then a commit in a public repository is scanned against every name
// in the index -- its message, every added or changed line, and every new path
// -- and a match refuses it. A commit in a confidential repository is not
// scanned: those names are its own.

// effectsFileWriter backs lifecycle.FileWriter with the dispatch's effects
// handle, so the index writes are recorded rather than performed under
// --dry-run, like every other mutation safegit makes.
type effectsFileWriter struct{ flags globalFlags }

func (w effectsFileWriter) WriteFile(p string, data []byte) error {
	_, err := w.flags.effects().Write(p, data)
	return err
}

func (w effectsFileWriter) MkdirAll(p string) error {
	_, err := w.flags.effects().Mkdir(p)
	return err
}

// mustCommitScreen is commitScreen for one dispatch: the index at its
// machine-local path, written through the dispatch's effects handle, and the
// record evaluated at today's date. Any failure refuses the commit before the
// pipeline runs.
func mustCommitScreen(flags globalFlags) commit.Screen {
	indexPath, err := index.DefaultPath()
	if err != nil {
		strictcli.ExitNow(exitcode.General, exitMessage(err))
	}
	screen, err := commitScreen(flags.ctx(), effectsFileWriter{flags}, indexPath, time.Now())
	if err != nil {
		strictcli.ExitNow(exitcode.General, exitMessage(err))
	}
	return screen
}

// commitScreen reads the repository's lifecycle-and-license record, brings the
// confidential-name index up to date for this repository through w, and
// returns the screen the commit pipeline runs: the name scan for a public
// repository, and the screen that scans nothing for a confidential one.
//
// on is the date the record is evaluated at, and indexPath the index file.
func commitScreen(ctx context.Context, w lifecycle.FileWriter, indexPath string, on time.Time) (commit.Screen, error) {
	root, err := git.AnchorRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolving the repository root to read its lifecycle-and-license record: %w", err)
	}
	record, err := lifecycle.Load(root)
	if err != nil {
		return nil, fmt.Errorf("reading the lifecycle-and-license record, which decides whether this commit is scanned for confidential names: %w", err)
	}
	origin, hasOrigin, err := git.ConfigGet(ctx, "remote.origin.url")
	if err != nil {
		return nil, fmt.Errorf("reading the origin remote, which keys this repository's entry in the confidential-name index: %w", err)
	}
	idx, err := index.Load(indexPath)
	if err != nil {
		return nil, fmt.Errorf("reading the confidential-name index: %w", err)
	}

	if record.Confidential(on) {
		if !hasOrigin {
			return nil, fmt.Errorf("this repository is confidential (a releasable has a proprietary license period in effect in %s), "+
				"so the names it protects belong in the confidential-name index at %s, which keys every entry by the repository's "+
				"origin remote, and this repository has no origin remote. Add it (git remote add origin <url>), then commit again",
				lifecycle.RecordFile, indexPath)
		}
		name, err := repositoryName(origin)
		if err != nil {
			return nil, err
		}
		names, err := record.ConfidentialNames(on, name)
		if err != nil {
			return nil, err
		}
		if err := idx.Upsert(w, origin, names); err != nil {
			return nil, fmt.Errorf("recording this repository's confidential names in the index at %s: %w", indexPath, err)
		}
		return confidentialRepositoryScreen{}, nil
	}

	if hasOrigin {
		if err := idx.Remove(w, origin); err != nil {
			return nil, fmt.Errorf("removing this public repository's entry from the confidential-name index at %s: %w", indexPath, err)
		}
	}
	return confidentialNameScan{names: idx.Names(), indexPath: indexPath}, nil
}

// repositoryName is the last path segment of the normalized origin, which is
// the name ConfidentialNames protects when no releasable carries a public
// license.
func repositoryName(origin string) (string, error) {
	norm, err := index.NormalizeOrigin(origin)
	if err != nil {
		return "", fmt.Errorf("reading the origin remote %q: %w", origin, err)
	}
	return path.Base(strings.TrimPrefix(norm, "file://")), nil
}

// confidentialRepositoryScreen is the screen of a confidential repository: it
// scans nothing, because the names the index holds for this repository are its
// own, and the names of other confidential repositories are not this one's to
// police.
type confidentialRepositoryScreen struct{}

func (confidentialRepositoryScreen) Inspect(context.Context, string, []git.ChangedPath) error {
	return nil
}

// confidentialNameScan is the screen of a public repository: the commit's
// message, its new paths, and every added or changed line of its delta are
// matched against every name in the index.
type confidentialNameScan struct {
	names     []string
	indexPath string
}

func (s confidentialNameScan) Inspect(ctx context.Context, message string, changed []git.ChangedPath) error {
	if len(s.names) == 0 {
		return nil
	}
	var findings []string
	for _, m := range index.ScanTerms(message, s.names) {
		findings = append(findings, fmt.Sprintf("the commit message, line %d, column %d: %s", m.Line, m.Column, m.Term))
	}

	var shas []string
	for _, c := range changed {
		if carriesBlob(c.SrcMode, c.SrcSHA) {
			shas = append(shas, c.SrcSHA)
		}
		if carriesBlob(c.DstMode, c.DstSHA) {
			shas = append(shas, c.DstSHA)
		}
	}
	blobs, err := readBlobs(ctx, shas)
	if err != nil {
		return fmt.Errorf("reading the commit's content to scan it for confidential names: %w", err)
	}

	for _, c := range changed {
		if c.Status == "A" {
			for _, m := range index.ScanTerms(c.Path, s.names) {
				findings = append(findings, fmt.Sprintf("%s: the new path, column %d: %s", c.Path, m.Column, m.Term))
			}
		}
		if !carriesBlob(c.DstMode, c.DstSHA) {
			continue
		}
		var before []byte
		if carriesBlob(c.SrcMode, c.SrcSHA) {
			before = blobs[c.SrcSHA]
		}
		for _, m := range addedLineMatches(before, blobs[c.DstSHA], s.names) {
			findings = append(findings, fmt.Sprintf("%s, line %d, column %d: %s", c.Path, m.Line, m.Column, m.Term))
		}
	}

	if len(findings) == 0 {
		return nil
	}
	return &commit.CommitError{
		Code: exitcode.General,
		Message: fmt.Sprintf("refusing to commit: this repository is public (its %s has no proprietary license period in effect, "+
			"or it has no such record), and the commit names terms the confidential-name index protects:\n  %s\n"+
			"Remove each term from the named lines, paths, and message, then commit again. "+
			"The index is %s; commits in the confidential repositories keep it current.",
			lifecycle.RecordFile, strings.Join(findings, "\n  "), s.indexPath),
	}
}

// carriesBlob reports whether one side of a raw delta entry names a blob: not
// the absent side of an addition or a deletion, and not a gitlink, whose
// object is a commit of another repository.
func carriesBlob(mode, sha string) bool {
	return sha != git.ZeroSHA && mode != git.ZeroMode && mode != "160000"
}

// readBlobs reads the named blobs through one cat-file process.
func readBlobs(ctx context.Context, shas []string) (map[string][]byte, error) {
	out := make(map[string][]byte, len(shas))
	var unique []string
	seen := make(map[string]bool, len(shas))
	for _, sha := range shas {
		if !seen[sha] {
			seen[sha] = true
			unique = append(unique, sha)
		}
	}
	if len(unique) == 0 {
		return out, nil
	}
	it, err := git.CatFileBatchSHAs(ctx, unique)
	if err != nil {
		return nil, err
	}
	defer it.Close()
	for {
		obj, err := it.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		out[obj.SHA] = obj.Content
	}
	for _, sha := range unique {
		if _, ok := out[sha]; !ok {
			return nil, fmt.Errorf("blob %s was not returned by cat-file", sha)
		}
	}
	return out, nil
}

// addedLineMatches scans after against names and keeps the matches on lines
// the change added or changed: a line of after counts as added unless the same
// line occurs in before, each line of before accounting for one occurrence.
// Lines are numbered in after. A nil before (a new file) makes every line
// added.
func addedLineMatches(before, after []byte, names []string) []index.Match {
	remaining := make(map[string]int)
	if before != nil {
		for _, line := range strings.Split(string(before), "\n") {
			remaining[line]++
		}
	}
	added := make(map[int]bool)
	for i, line := range strings.Split(string(after), "\n") {
		if remaining[line] > 0 {
			remaining[line]--
			continue
		}
		added[i+1] = true
	}
	var out []index.Match
	for _, m := range index.ScanTerms(string(after), names) {
		if added[m.Line] {
			out = append(out, m)
		}
	}
	return out
}
