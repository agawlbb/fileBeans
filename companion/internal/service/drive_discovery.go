package service

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
)

const driveDiscoveryWorkers = 4

type driveCopyNode struct {
	file     File
	id       string
	children []*driveCopyNode
}
type driveFolderResult struct {
	node     *driveCopyNode
	children []DriveItem
	err      error
}

// Folder listing overlaps across four requests, but assembly stays deterministic
// and parent-first. The coordinator alone owns duplicate detection and the tree.
func (s *Service) discoverDriveTree(parent context.Context, top DriveItem, connection string, withinDrive bool) ([]File, error) {
	ctx, cancel := context.WithCancel(parent)
	tasks := make(chan *driveCopyNode)
	results := make(chan driveFolderResult, driveDiscoveryWorkers)
	var workers sync.WaitGroup
	defer func() { cancel(); close(tasks); workers.Wait() }()
	for i := 0; i < driveDiscoveryWorkers; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for node := range tasks {
				result := driveFolderResult{node: node}
				page := ""
				pages := map[string]bool{}
				for {
					list, err := s.driveList(ctx, node.id, page, connection)
					if err != nil {
						result.err = err
						break
					}
					if list.Incomplete {
						result.err = errors.New("Drive folder listing is incomplete")
						break
					}
					if ctx.Value(peerInventoryKey{}) != nil && len(result.children)+len(list.Files) > piManifestLimit {
						result.err = ErrUnsafe
						break
					}
					result.children = append(result.children, list.Files...)
					if list.Next == "" {
						break
					}
					if pages[list.Next] {
						result.err = ErrUnsafe
						break
					}
					pages[list.Next] = true
					page = list.Next
				}
				select {
				case results <- result:
				case <-ctx.Done():
					return
				}
			}
		}()
	}
	seen := map[string]bool{}
	makeNode := func(item DriveItem, path string) (*driveCopyNode, error) {
		if !validName(item.Name) || !driveID(item.ID) {
			return nil, ErrUnsafe
		}
		if ctx.Value(peerInventoryKey{}) != nil && len(seen) >= piManifestLimit {
			return nil, ErrUnsafe
		}
		if seen[item.ID] {
			return nil, ErrUnsafe
		}
		seen[item.ID] = true
		dir := item.Mime == "application/vnd.google-apps.folder"
		if !dir && ((!withinDrive && (strings.HasPrefix(item.Mime, "application/vnd.google-apps.") || !item.Capabilities.Download)) || (withinDrive && !item.Capabilities.Copy)) {
			return nil, ErrUnsupported
		}
		var size int64
		if !dir && item.Size != "" {
			var err error
			size, err = strconv.ParseInt(item.Size, 10, 64)
			if err != nil || size < 0 {
				return nil, ErrUnsafe
			}
		}
		checksum := ""
		if !dir {
			if item.SHA256 != "" {
				checksum = "sha256:" + item.SHA256
			} else if item.MD5 != "" {
				checksum = "md5:" + item.MD5
			}
		}
		return &driveCopyNode{id: item.ID, file: File{Source: "drive", From: item.ID, To: path, Size: size, Version: item.Version, Directory: dir, Mime: item.Mime, Hash: checksum}}, nil
	}
	root, err := makeNode(top, top.Name)
	if err != nil {
		return nil, err
	}
	pending := []*driveCopyNode{}
	if root.file.Directory {
		pending = append(pending, root)
	}
	active := 0
	for len(pending) > 0 || active > 0 {
		var send chan *driveCopyNode
		var next *driveCopyNode
		if len(pending) > 0 {
			send = tasks
			next = pending[0]
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case send <- next:
			pending = pending[1:]
			active++
		case result := <-results:
			active--
			if result.err != nil {
				return nil, result.err
			}
			names := map[string]bool{}
			for _, item := range result.children {
				if names[item.Name] {
					return nil, ErrConflict
				}
				names[item.Name] = true
				node, err := makeNode(item, filepath.Join(result.node.file.To, item.Name))
				if err != nil {
					return nil, err
				}
				result.node.children = append(result.node.children, node)
				if node.file.Directory {
					pending = append(pending, node)
				}
			}
		}
	}
	var files []File
	var flatten func(*driveCopyNode)
	flatten = func(node *driveCopyNode) {
		files = append(files, node.file)
		for _, child := range node.children {
			flatten(child)
		}
	}
	flatten(root)
	return files, nil
}
