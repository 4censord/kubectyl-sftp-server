package sftp

import (
	"io"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"emperror.dev/errors"
	"github.com/apex/log"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/kubectyl/sftp-server/config"
)

const (
	PermissionFileRead        = "file.read"
	PermissionFileReadContent = "file.read-content"
	PermissionFileCreate      = "file.create"
	PermissionFileUpdate      = "file.update"
	PermissionFileDelete      = "file.delete"
)

type Handler struct {
	mu          sync.Mutex
	fs          string
	permissions []string
	logger      *log.Entry
	ro          bool
}

// NewHandler returns a new connection handler for the SFTP server. This allows a given user
// to access the underlying filesystem.
func NewHandler(sc *ssh.ServerConn) (*Handler, error) {
	uuid, ok := sc.Permissions.Extensions["user"]
	if !ok {
		return nil, errors.New("sftp: mismatched Wings and Panel versions — Panel 1.10 is required for this version of Wings.")
	}

	return &Handler{
		permissions: strings.Split(sc.Permissions.Extensions["permissions"], ","),
		fs:          config.Get().System.Data + "/" + sc.Permissions.Extensions["uuid"],
		ro:          config.Get().System.Sftp.ReadOnly,
		logger:      log.WithFields(log.Fields{"subsystem": "sftp", "user": uuid, "ip": sc.RemoteAddr()}),
	}, nil
}

// Handlers returns the sftp.Handlers for this struct.
func (h *Handler) Handlers() sftp.Handlers {
	return sftp.Handlers{
		FileGet:  h,
		FilePut:  h,
		FileCmd:  h,
		FileList: h,
	}
}

// Fileread creates a reader for a file on the system and returns the reader back.
func (h *Handler) Fileread(request *sftp.Request) (io.ReaderAt, error) {
	// Check first if the user can actually open and view a file. This permission is named
	// really poorly, but it is checking if they can read. There is an addition permission,
	// "save-files" which determines if they can write that file.
	if !h.can(PermissionFileReadContent) {
		return nil, sftp.ErrSSHFxPermissionDenied
	}

	p, err := h.buildPath(request.Filepath)
	if err != nil {
		return nil, sftp.ErrSshFxNoSuchFile
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	f, err := os.Open(p)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			h.logger.WithField("error", err).Error("error processing readfile request")
			return nil, sftp.ErrSSHFxFailure
		}
		return nil, sftp.ErrSSHFxNoSuchFile
	}
	return f, nil
}

// Filewrite handles the write actions for a file on the system.
func (h *Handler) Filewrite(request *sftp.Request) (io.WriterAt, error) {
	if h.ro {
		return nil, sftp.ErrSSHFxOpUnsupported
	}
	l := h.logger.WithField("source", request.Filepath)

	p, err := h.buildPath(request.Filepath)
	if err != nil {
		return nil, sftp.ErrSshFxNoSuchFile
	}

	// If the user doesn't have enough space left on the server it should respond with an
	// error since we won't be letting them write this file to the disk.

	// if !h.fs.HasSpaceAvailable(true) {
	// 	return nil, ErrSSHQuotaExceeded
	// }

	h.mu.Lock()
	defer h.mu.Unlock()
	// The specific permission required to perform this action. If the file exists on the
	// system already it only needs to be an update, otherwise we'll check for a create.
	permission := PermissionFileUpdate
	_, sterr := os.Stat(p)
	if sterr != nil {
		if !errors.Is(sterr, os.ErrNotExist) {
			l.WithField("error", sterr).Error("error while getting file reader")
			return nil, sftp.ErrSSHFxFailure
		}
		permission = PermissionFileCreate
	}
	// Confirm the user has permission to perform this action BEFORE calling Touch, otherwise
	// you'll potentially create a file on the system and then fail out because of user
	// permission checking after the fact.
	if !h.can(permission) {
		return nil, sftp.ErrSSHFxPermissionDenied
	}

	// Create all of the directories leading up to the location where this file is being created.
	if err := os.MkdirAll(filepath.Dir(p), 0755); err != nil {
		l.WithField("error", err).Error("error making path for file")
		return nil, sftp.ErrSshFxFailure
	}

	file, err := os.Create(p)
	if err != nil {
		l.WithField("error", err).Error("error creating file")
		return nil, sftp.ErrSshFxFailure
	}

	// Not failing here is intentional. We still made the file, it is just owned incorrectly
	// and will likely cause some issues.
	if err := os.Chown(p, 0, 0); err != nil {
		l.WithField("error", err).Error("error chowning file")
	}
	return file, nil
}

// Filecmd hander for basic SFTP system calls related to files, but not anything to do with reading
// or writing to those files.
func (h *Handler) Filecmd(request *sftp.Request) error {
	if h.ro {
		return sftp.ErrSSHFxOpUnsupported
	}

	p, err := h.buildPath(request.Filepath)
	if err != nil {
		return sftp.ErrSshFxNoSuchFile
	}

	var target string
	// If a target is provided in this request validate that it is going to the correct
	// location for the server. If it is not, return an operation unsupported error. This
	// is maybe not the best error response, but its not wrong either.
	if request.Target != "" {
		target, err = h.buildPath(request.Target)
		if err != nil {
			return sftp.ErrSshFxOpUnsupported
		}
	}

	l := h.logger.WithField("source", request.Filepath)
	if request.Target != "" {
		l = l.WithField("target", request.Target)
	}

	switch request.Method {
	case "Setstat":
		var mode os.FileMode = 0644

		// If the client passed a valid file permission use that, otherwise use the
		// default of 0644 set above.
		if request.Attributes().FileMode().Perm() != 0000 {
			mode = request.Attributes().FileMode().Perm()
		}

		// Force directories to be 0755
		if request.Attributes().FileMode().IsDir() {
			mode = 0755
		}

		if err := os.Chmod(p, mode); err != nil {
			l.WithField("error", err).Error("failed to perform setstat on item")
			return sftp.ErrSshFxFailure
		}
		return nil
	case "Rename":
		if !h.can(PermissionFileUpdate) {
			return sftp.ErrSshFxPermissionDenied
		}

		if err := os.Rename(p, target); err != nil {
			l.WithField("error", err).Error("failed to rename file")
			return sftp.ErrSshFxFailure
		}
	case "Rmdir":
		if !h.can(PermissionFileDelete) {
			return sftp.ErrSshFxPermissionDenied
		}

		if err := os.RemoveAll(p); err != nil {
			l.WithField("error", err).Error("failed to remove directory")
			return sftp.ErrSshFxFailure
		}

		return sftp.ErrSshFxOk
	case "Mkdir":
		if !h.can(PermissionFileCreate) {
			return sftp.ErrSshFxPermissionDenied
		}

		if err := os.MkdirAll(p, 0755); err != nil {
			l.WithField("error", err).Error("failed to create directory")
			return sftp.ErrSshFxFailure
		}
	case "Symlink":
		if !h.can(PermissionFileCreate) {
			return sftp.ErrSshFxPermissionDenied
		}

		if err := os.Symlink(p, target); err != nil {
			l.WithField("target", target).WithField("error", err).Error("failed to create symlink")
			return sftp.ErrSshFxFailure
		}
	case "Remove":
		if !h.can(PermissionFileDelete) {
			return sftp.ErrSshFxPermissionDenied
		}

		if err := os.Remove(p); err != nil {
			l.WithField("error", err).Error("failed to remove a file")
			return sftp.ErrSshFxFailure
		}

		return sftp.ErrSshFxOk
	default:
		return sftp.ErrSSHFxOpUnsupported
	}

	var fileLocation = p
	if target != "" {
		fileLocation = target
	}

	// Not failing here is intentional. We still made the file, it is just owned incorrectly
	// and will likely cause some issues. There is no logical check for if the file was removed
	// because both of those cases (Rmdir, Remove) have an explicit return rather than break.
	if err := os.Chown(fileLocation, 0, 0); err != nil {
		l.WithField("error", err).Warn("error chowning file")
	}

	return sftp.ErrSSHFxOk
}

// Filelist is the handler for SFTP filesystem list calls. This will handle calls to list the contents of
// a directory as well as perform file/folder stat calls.
func (h *Handler) Filelist(request *sftp.Request) (sftp.ListerAt, error) {
	if !h.can(PermissionFileRead) {
		return nil, sftp.ErrSSHFxPermissionDenied
	}

	p, err := h.buildPath(request.Filepath)
	if err != nil {
		return nil, sftp.ErrSSHFxNoSuchFile
	}

	switch request.Method {
	case "List":
		files, err := ioutil.ReadDir(p)
		if err != nil {
			h.logger.WithField("source", request.Filepath).WithField("error", err).Error("error while listing directory")
			return nil, sftp.ErrSSHFxFailure
		}
		return ListerAt(files), nil
	case "Stat":
		st, err := os.Stat(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil, sftp.ErrSSHFxNoSuchFile
			}
			h.logger.WithField("source", request.Filepath).WithField("error", err).Error("error performing stat on file")
			return nil, sftp.ErrSSHFxFailure
		}
		return ListerAt([]os.FileInfo{st}), nil
	default:
		return nil, sftp.ErrSSHFxOpUnsupported
	}
}

// Determines if a user has permission to perform a specific action on the SFTP server. These
// permissions are defined and returned by the Panel API.
func (h *Handler) can(permission string) bool {
	// if h.server.IsSuspended() {
	// 	return false
	// }
	for _, p := range h.permissions {
		// If we match the permission specifically, or the user has been granted the "*"
		// permission because they're an admin, let them through.
		if p == permission || p == "*" {
			return true
		}
	}
	return false
}

// Normalizes a directory we get from the SFTP request to ensure the user is not able to escape
// from their data directory. After normalization if the directory is still within their home
// path it is returned. If they managed to "escape" an error will be returned.
func (h *Handler) buildPath(rawPath string) (string, error) {
	var nonExistentPathResolution string
	// Calling filepath.Clean on the joined directory will resolve it to the absolute path,
	// removing any ../ type of path resolution, and leaving us with a direct path link.
	r := filepath.Clean(filepath.Join(h.fs, rawPath))

	// At the same time, evaluate the symlink status and determine where this file or folder
	// is truly pointing to.
	p, err := filepath.EvalSymlinks(r)
	if err != nil && !os.IsNotExist(err) {
		return "", err
	} else if os.IsNotExist(err) {
		// The requested directory doesn't exist, so at this point we need to iterate up the
		// path chain until we hit a directory that _does_ exist and can be validated.
		parts := strings.Split(filepath.Dir(r), "/")

		var try string
		// Range over all of the path parts and form directory pathings from the end
		// moving up until we have a valid resolution or we run out of paths to try.
		for k := range parts {
			try = strings.Join(parts[:(len(parts)-k)], "/")

			if !strings.HasPrefix(try, h.fs) {
				break
			}

			t, err := filepath.EvalSymlinks(try)
			if err == nil {
				nonExistentPathResolution = t
				break
			}
		}
	}

	// If the new path doesn't start with their root directory there is clearly an escape
	// attempt going on, and we should NOT resolve this path for them.
	if nonExistentPathResolution != "" {
		if !strings.HasPrefix(nonExistentPathResolution, h.fs) {
			return "", errors.New("invalid path resolution")
		}

		// If the nonExistentPathResoltion variable is not empty then the initial path requested
		// did not exist and we looped through the pathway until we found a match. At this point
		// we've confirmed the first matched pathway exists in the root server directory, so we
		// can go ahead and just return the path that was requested initially.
		return r, nil
	}

	// If the requested directory from EvalSymlinks begins with the server root directory go
	// ahead and return it. If not we'll return an error which will block any further action
	// on the file.
	if strings.HasPrefix(p, h.fs) {
		return p, nil
	}

	return "", errors.New("invalid path resolution")
}
