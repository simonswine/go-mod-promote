package errors

type ErrNotImplemented struct {
}

func (ErrNotImplemented) Error() string {
	return "Not implemented"
}
