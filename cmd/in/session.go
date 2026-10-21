package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const sessionAliasLimit = 128

// sessionBinding keeps the oldest-to-newest authenticated tuples for one ID.
type sessionBinding struct {
	ID     string   `json:"id"`
	Tuples []string `json:"tuples"`
}

// sessionAlias indexes an exact authenticated tuple to its owning ID.
type sessionAlias struct {
	Tuple string `json:"tuple"`
	ID    string `json:"id"`
}

// sessionDirectory returns the private directory containing only logical SSH bindings.
func sessionDirectory(root string) (string, error) {
	if err := privateDir(root); err != nil {
		return "", err
	}
	dir := filepath.Join(root, "session")
	if err := privateDir(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// sessionID accepts only the lowercase, 128-bit random IDs used for logical bindings.
func sessionID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if c < '0' || c > '9' {
			if c < 'a' || c > 'f' {
				return false
			}
		}
	}
	return true
}

// canonicalTuple accepts an exact numeric SSH_CONNECTION with canonical addresses and ports.
func canonicalTuple(tuple string) bool {
	fields := strings.Split(tuple, " ")
	if len(fields) != 4 {
		return false
	}
	for _, i := range []int{0, 2} {
		addr, err := netip.ParseAddr(fields[i])
		if err != nil || addr.Zone() != "" || addr.String() != fields[i] {
			return false
		}
	}
	for _, i := range []int{1, 3} {
		port, err := strconv.Atoi(fields[i])
		if err != nil || port < 1 || port > 65535 || strconv.Itoa(port) != fields[i] {
			return false
		}
	}
	return true
}

// sessionFile reads one bounded, owned JSON record and refuses unknown or modified state.
func sessionFile(path string, dst any) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 16384 {
		return false, fmt.Errorf("invalid session binding record")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		return false, fmt.Errorf("invalid session binding record")
	}
	data, exists, err := readOwned(path)
	if err != nil || !exists || len(data) > 16384 {
		return false, fmt.Errorf("invalid session binding record")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if dec.Decode(dst) != nil || dec.Decode(new(any)) != io.EOF {
		return false, fmt.Errorf("invalid session binding record")
	}
	return true, nil
}

// writeSessionFile replaces a validated binding record without exposing its contents in errors.
func writeSessionFile(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil || len(data) > 16384 {
		return fmt.Errorf("invalid session binding record")
	}
	if err := atomicWrite(path, data, 0600); err != nil {
		return fmt.Errorf("cannot write session binding record")
	}
	return nil
}

// localSession reads or creates the stable random ID for one authenticated control socket.
// The coordinator lock for this peer must be held by the caller.
func localSession(root string, p peer) (string, error) {
	dir, err := sessionDirectory(root)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, "binding-key-"+digest(p.Control)+".json")
	var record struct {
		ID string `json:"id"`
	}
	exists, err := sessionFile(path, &record)
	if err != nil {
		return "", err
	}
	if exists {
		if !sessionID(record.ID) {
			return "", fmt.Errorf("invalid session binding record")
		}
		return record.ID, nil
	}
	record.ID = requestID()
	if err := writeSessionFile(path, record); err != nil {
		return "", err
	}
	return record.ID, nil
}

// sessionMutationLock serializes alias writes and lookups across relay processes.
func sessionMutationLock(dir string, shared bool) (*os.File, error) {
	path := filepath.Join(dir, ".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, fmt.Errorf("cannot lock session bindings")
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		f.Close()
		return nil, fmt.Errorf("invalid session binding lock")
	}
	if st, ok := info.Sys().(*syscall.Stat_t); !ok || int(st.Uid) != os.Getuid() {
		f.Close()
		return nil, fmt.Errorf("invalid session binding lock")
	}
	how := syscall.LOCK_EX
	if shared {
		how = syscall.LOCK_SH
	}
	until := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Flock(int(f.Fd()), how|syscall.LOCK_NB)
		if err == nil {
			break
		}
		if (!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN)) || time.Now().After(until) {
			f.Close()
			return nil, fmt.Errorf("cannot lock session bindings")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return f, nil
}

// readBinding validates one ID's ordered aliases; the oldest is evicted at 128.
func readBinding(dir, id string) (sessionBinding, bool, error) {
	var b sessionBinding
	exists, err := sessionFile(filepath.Join(dir, "binding-"+id+".json"), &b)
	if err != nil || !exists {
		return b, exists, err
	}
	if b.ID != id || len(b.Tuples) == 0 || len(b.Tuples) > sessionAliasLimit {
		return b, false, fmt.Errorf("invalid session binding record")
	}
	seen := make(map[string]bool, len(b.Tuples))
	for _, tuple := range b.Tuples {
		if !canonicalTuple(tuple) || seen[tuple] {
			return b, false, fmt.Errorf("invalid session binding record")
		}
		seen[tuple] = true
	}
	return b, true, nil
}

// readAlias checks that a digest-named index contains its exact tuple and valid ID.
func readAlias(dir, tuple string) (sessionAlias, bool, error) {
	var a sessionAlias
	exists, err := sessionFile(filepath.Join(dir, "alias-"+digest(tuple)+".json"), &a)
	if err != nil || !exists {
		return a, exists, err
	}
	if a.Tuple != tuple || !sessionID(a.ID) {
		return a, false, fmt.Errorf("invalid session alias record")
	}
	return a, true, nil
}

// bindSession adds an authenticated tuple to an ID, retaining at most 128 aliases.
// It writes the new index before the binding and removes only an evicted owned index.
func bindSession(root, id, tuple string) error {
	if !sessionID(id) || !canonicalTuple(tuple) {
		return fmt.Errorf("invalid session binding")
	}
	dir, err := sessionDirectory(root)
	if err != nil {
		return err
	}
	guard, err := sessionMutationLock(dir, false)
	if err != nil {
		return err
	}
	defer guard.Close()
	b, exists, err := readBinding(dir, id)
	if err != nil {
		return err
	}
	a, indexed, err := readAlias(dir, tuple)
	if err != nil {
		return err
	}
	if indexed && a.ID != id {
		return fmt.Errorf("session alias already belongs to another binding")
	}
	if !exists {
		b = sessionBinding{ID: id}
	}
	for _, old := range b.Tuples {
		if old == tuple {
			if !indexed {
				return fmt.Errorf("session alias index is missing")
			}
			return nil
		}
	}
	var evicted string
	if len(b.Tuples) == sessionAliasLimit {
		evicted = b.Tuples[0]
		old, found, err := readAlias(dir, evicted)
		if err != nil || !found || old.ID != id {
			return fmt.Errorf("invalid session alias record")
		}
	}
	if !indexed {
		if err := writeSessionFile(filepath.Join(dir, "alias-"+digest(tuple)+".json"), sessionAlias{Tuple: tuple, ID: id}); err != nil {
			return err
		}
	}
	if evicted != "" {
		b.Tuples = b.Tuples[1:]
	}
	b.Tuples = append(b.Tuples, tuple)
	if err := writeSessionFile(filepath.Join(dir, "binding-"+id+".json"), b); err != nil {
		return err
	}
	if evicted != "" {
		if err := os.Remove(filepath.Join(dir, "alias-"+digest(evicted)+".json")); err != nil {
			return fmt.Errorf("cannot remove expired session alias")
		}
	}
	return nil
}

// sessionIdentity resolves an exact tuple alias or preserves the unbound tuple as before.
func sessionIdentity(root, tuple string) (string, error) {
	if !canonicalTuple(tuple) {
		return "", fmt.Errorf("no current SSH connection for this browser request")
	}
	dir, err := sessionDirectory(root)
	if err != nil {
		return "", err
	}
	guard, err := sessionMutationLock(dir, true)
	if err != nil {
		return "", err
	}
	defer guard.Close()
	a, exists, err := readAlias(dir, tuple)
	if err != nil {
		return "", err
	}
	if !exists {
		return tuple, nil
	}
	b, found, err := readBinding(dir, a.ID)
	if err != nil || !found {
		return "", fmt.Errorf("invalid session binding record")
	}
	for _, old := range b.Tuples {
		if old == tuple {
			return "session:" + a.ID, nil
		}
	}
	return "", fmt.Errorf("invalid session alias record")
}
