package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"regexp"
	"strings"
)

const (
	identityOwnerPrefix = "# qubes-air-owner: "
	maxIdentityBytes    = 1 << 20
	maxIdentityEntries  = 100000
)

var identityIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,95}$`)

func (c *CertIssuer) lockIdentity(ctx context.Context) error {
	c.identityOnce.Do(func() { c.identityGate = make(chan struct{}, 1) })
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case c.identityGate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *CertIssuer) unlockIdentity() { <-c.identityGate }

func validateIdentityOwner(id, name string) error {
	if !identityIDPattern.MatchString(id) {
		return errors.New("invalid identity owner ID")
	}
	return validateQubeName(name)
}

func markIdentityOwner(id, name, document string) string {
	return strings.Replace(document, "#cloud-config\n",
		"#cloud-config\n"+identityOwnerPrefix+id+" "+name+"\n", 1)
}

type ownedIdentity struct {
	name string
	info os.FileInfo
}

// RemoveIdentityFor removes only verified delivery documents after the provider
// has confirmed destruction. The configured directory must be trusted: these
// public markers bind ownership, but do not authenticate a hostile filesystem.
// Unknown files fail closed for operator review; missing files are idempotent.
func (c *CertIssuer) RemoveIdentityFor(ctx context.Context, id, name string) error {
	if c == nil || c.identityDir == "" {
		return nil
	}
	if err := validateIdentityOwner(id, name); err != nil {
		return err
	}
	if err := c.lockIdentity(ctx); err != nil {
		return err
	}
	defer c.unlockIdentity()
	root, err := os.OpenRoot(c.identityDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open identity directory: %w", err)
	}
	defer root.Close()
	files, err := collectOwnedIdentities(ctx, root, id, name)
	if err != nil {
		return err
	}
	for i, file := range files {
		if err := removeOwnedIdentity(ctx, root, file); err != nil {
			return fmt.Errorf("identity cleanup removed %d of %d files: %w", i, len(files), err)
		}
	}
	log.Printf("pki: removed %d identity delivery file(s) for qube %q", len(files), name)
	return nil
}

func collectOwnedIdentities(ctx context.Context, root *os.Root, id, name string) ([]ownedIdentity, error) {
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	pattern := snippetNamePattern(name)
	var files []ownedIdentity
	for scanned := 0; ; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		entries, err := dir.ReadDir(256)
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("list identity directory: %w", err)
		}
		scanned += len(entries)
		if scanned > maxIdentityEntries {
			return nil, errors.New("identity directory exceeds cleanup entry limit")
		}
		for _, entry := range entries {
			if !pattern.MatchString(entry.Name()) {
				continue
			}
			file, owned, err := inspectIdentity(root, entry.Name(), id, name)
			if err != nil {
				return nil, fmt.Errorf("identity %q requires ownership review: %w", entry.Name(), err)
			}
			if owned {
				files = append(files, file)
			}
		}
		if errors.Is(err, io.EOF) {
			return files, nil
		}
	}
}

func inspectIdentity(root *os.Root, file, id, name string) (ownedIdentity, bool, error) {
	data, info, err := readIdentity(root, file)
	if err != nil {
		return ownedIdentity{}, false, err
	}
	ownerID, ownerName, err := parseIdentityOwner(data)
	if err != nil {
		return ownedIdentity{}, false, err
	}
	if file != SnippetFileName(ownerName) && file != ContentAddressedSnippetName(ownerName, string(data)) {
		return ownedIdentity{}, false, errors.New("filename does not match the marked owner and content")
	}
	// A local file for name-<12hex> also matches name's shared-file pattern.
	// Its validated marker proves it belongs to a different qube; keep it.
	if ownerName != name {
		return ownedIdentity{}, false, nil
	}
	// Lookup has only the immutable qube name. Destructive callers always
	// supply an ID and must match it as well.
	if id != "" && ownerID != id {
		return ownedIdentity{}, false, errors.New("qube ID does not match ownership marker")
	}
	return ownedIdentity{name: file, info: info}, true, nil
}

func parseIdentityOwner(data []byte) (string, string, error) {
	lines := strings.SplitN(string(data), "\n", 3)
	if len(lines) != 3 || lines[0] != "#cloud-config" || !strings.HasPrefix(lines[1], identityOwnerPrefix) {
		return "", "", errors.New("missing ownership marker; retained for manual reconciliation")
	}
	owner := strings.Split(strings.TrimPrefix(lines[1], identityOwnerPrefix), " ")
	if len(owner) != 2 || validateIdentityOwner(owner[0], owner[1]) != nil {
		return "", "", errors.New("invalid ownership marker")
	}
	return owner[0], owner[1], nil
}

func readIdentity(root *os.Root, name string) ([]byte, os.FileInfo, error) {
	if !snippetFileRE.MatchString(name) || strings.Contains(name, "..") {
		return nil, nil, errors.New("invalid identity filename")
	}
	info, err := root.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, nil, errors.New("identity is not a regular file")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, nil, errors.New("identity changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(f, maxIdentityBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > maxIdentityBytes {
		return nil, nil, errors.New("identity exceeds cleanup size limit")
	}
	return data, info, nil
}

func removeOwnedIdentity(ctx context.Context, root *os.Root, file ownedIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now, err := root.Lstat(file.name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !now.Mode().IsRegular() || !os.SameFile(file.info, now) ||
		file.info.Size() != now.Size() || !file.info.ModTime().Equal(now.ModTime()) {
		return errors.New("identity changed after ownership verification")
	}
	return root.Remove(file.name)
}
