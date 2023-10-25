package jsonrpc

type Error struct {
	code int
	message string
	data    any   
}

func (e *Error) Code() int {
	return e.code
}

func (e *Error) Data() any {
	return e.data
}

func (e *Error) Error() string {
	return e.message
}