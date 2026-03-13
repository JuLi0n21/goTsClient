package gotsclient

type Api struct {
}

type ReturnValue struct {
	Field  []string
	Second int
	Uhhhh  *uint32
}

type ReceiveValue struct {
	Field  []string
	Second int
	Uhhhh  *uint32
}

func (a Api) EmptyParams() (string, error) {
	return "", nil
}

func (a Api) Structs(value ReceiveValue) (ReturnValue, error) {
	return ReturnValue{}, nil
}

func (a Api) ComplexThings(items []string) (map[string]int, error) {
	return map[string]int{}, nil
}

func (a Api) ManyParams(b, c, d, e, f, g, h, i, j int) (string, error) {
	return "", nil
}
