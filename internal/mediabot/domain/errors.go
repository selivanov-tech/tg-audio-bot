package domain

import "errors"

var (
	ErrUnsupportedURL  = errors.New("unsupported url: no provider matches")
	ErrFileTooLarge    = errors.New("downloaded file exceeds size limit")
	ErrDownloadFailed  = errors.New("download failed")
	ErrInvalidState    = errors.New("invalid state transition")
)
