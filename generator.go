package rpc

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
)

//go:embed api.ts
var backendClient string

var errorType = reflect.TypeFor[error]()

func typeOf[T any]() reflect.Type {
	t := reflect.TypeFor[T]()
	if t.Kind() != reflect.Pointer && t.Kind() != reflect.Interface {
		t = reflect.PointerTo(t)
	}
	return t
}

func broadcastTypeOf[T any]() reflect.Type {
	t := reflect.TypeFor[T]()
	if t.Kind() == reflect.Interface {
		return nil
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t.Kind() == reflect.Struct && t.NumField() == 0 {
		return nil
	}
	return t
}

func GenClientType[API any, Broadcasts any](outPutPath string) error {
	ts, err := GenerateTSType[API, Broadcasts]("RPCClient")
	if err != nil {
		return err
	}
	return os.WriteFile(outPutPath, []byte(ts), 0644)
}

func GenerateTSType[API any, Broadcasts any](clientName string) (string, error) {
	return GenerateTS(typeOf[API](), broadcastTypeOf[Broadcasts](), clientName)
}

func GenClient(api any, broadcasts any, outPutPath string) error {
	var bt reflect.Type
	if broadcasts != nil {
		bt = reflect.TypeOf(broadcasts)
	}
	ts, err := GenerateTS(reflect.TypeOf(api), bt, "RPCClient")
	if err != nil {
		return err
	}
	return os.WriteFile(outPutPath, []byte(ts), 0644)
}

func GenerateTS(apiType reflect.Type, broadcastType reflect.Type, clientName string) (string, error) {
	var b strings.Builder
	structs := map[string]reflect.Type{}

	b.WriteString("export interface BroadcastEvents {\n")

	if broadcastType != nil {
		bt := broadcastType
		if bt.Kind() == reflect.Pointer {
			bt = bt.Elem()
		}

		if bt.Kind() == reflect.Struct {
			for f := range bt.Fields() {
				topicName := f.Name
				tag := f.Tag.Get("json")
				if tag != "" {
					topicName = strings.Split(tag, ",")[0]
				}
				collectStructs(f.Type, structs)
				fmt.Fprintf(&b, "  '%s': %s;\n", topicName, goTypeToTS(f.Type))

			}
		}
	}
	b.WriteString("}\n\n")

	b.WriteString("export type BroadcastTopic = keyof BroadcastEvents;\n\n")

	for method := range apiType.Methods() {
		i := -1
		for param := range method.Type.Ins() {
			i++
			if i == 0 {
				continue
			}
			collectStructs(param, structs)
		}

		outParams := method.Type.NumOut()
		unsupportedMethod := "not supported, allowed layout are \nfunc() (error) \nfunc() (struct, error), \nfunc(x...) (struct, error)"

		if outParams == 0 {
			return "", errors.New(method.Name + unsupportedMethod)
		}
		if outParams == 1 {
			if method.Type.Out(0).Implements(errorType) || method.Type.Out(0).Kind() == reflect.Chan {
				collectStructs(method.Type.Out(0), structs)
			} else {
				return "", errors.New(method.Name + unsupportedMethod)
			}
		}
		if outParams > 1 && (method.Type.Out(1).Implements(errorType) || method.Type.Out(0).Kind() == reflect.Chan) {
			collectStructs(method.Type.Out(0), structs)
		}
		if outParams > 2 {
			return "", errors.New(method.Name + unsupportedMethod)
		}
	}

	names := make([]string, 0, len(structs))
	for n := range structs {
		names = append(names, n)
	}
	slices.Sort(names)

	for _, name := range names {
		st := structs[name]
		fmt.Fprintf(&b, "export interface %s {\n", name)
		for f := range st.Fields() {
			jsName := f.Name
			tag := f.Tag.Get("json")
			if tag != "" {
				parts := strings.Split(tag, ",")
				if parts[0] != "" {
					jsName = parts[0]
				}
			}

			optional := ""
			ft := f.Type
			if ft.Kind() == reflect.Pointer {
				optional = "?"
				ft = ft.Elem()
			}
			fmt.Fprintf(&b, "  %s%s: %s;\n", jsName, optional, goTypeToTS(ft))
		}
		b.WriteString("}\n\n")
	}

	b.WriteString("export type RPCResult<T> = { data: T; error?: any };\n\n")
	b.WriteString("export type EntityRef = { type: string; id: string };\n\n")

	fmt.Fprintf(&b, "export interface %s {\n", clientName)
	for m := range apiType.Methods() {
		params := []string{}

		tsParamIdx := 0
		idx := -1
		for ptypeGo := range m.Type.Ins() {
			idx++
			if idx == 0 {
				continue
			}
			if ptypeGo.Implements(reflect.TypeOf((*interface{ Done() <-chan struct{} })(nil)).Elem()) ||
				strings.Contains(ptypeGo.String(), "context.Context") {
				continue
			}

			var pname string
			if ptypeGo.Kind() == reflect.Struct && ptypeGo.Name() != "" {
				pname = strings.ToLower(ptypeGo.Name()[:1]) + ptypeGo.Name()[1:]
			} else {
				pname = fmt.Sprintf("arg%d", tsParamIdx)
			}

			ptype := goTypeToTS(ptypeGo)
			params = append(params, fmt.Sprintf("%s: %s", pname, ptype))
			tsParamIdx++
		}
		params = append(params, "refs?: EntityRef[]")

		if m.Type.NumOut() > 0 && m.Type.Out(0).Kind() == reflect.Chan {
			resType := goTypeToTS(m.Type.Out(0).Elem())
			fmt.Fprintf(&b, "  %s(%s): Promise<RPCResult<%s>> & AsyncIterable<%s>;\n", m.Name, strings.Join(params, ", "), resType, resType)
			continue
		}

		resType := "any"
		if m.Type.NumOut() > 0 {
			resType = goTypeToTS(m.Type.Out(0))
			if m.Type.Out(0).Implements(errorType) {
				resType = "any"
			}
		}

		fmt.Fprintf(&b, "  %s(%s): Promise<RPCResult<%s>>;\n", m.Name, strings.Join(params, ", "), resType)
	}
	b.WriteString("}\n\n")

	b.WriteString(backendClient)

	b.WriteString("\nlet backendWrapper: WSBackend | null = null;\n")
	fmt.Fprintf(&b, "let backendInstance: %s | null = null;\n\n", clientName)
	fmt.Fprintf(&b, "export function useBackend(url: string = '/ws'): { api: %s, backend: WSBackend } {\n", clientName)
	b.WriteString("  if (url.startsWith('/')) {\n")
	b.WriteString("    const protocol = window.location.protocol === 'https:' ? 'wss' : 'ws';\n")
	b.WriteString("    const host = window.location.host;\n")
	b.WriteString("    url = `${protocol}://${host}${url}`;\n")
	b.WriteString("  }\n\n")
	b.WriteString("  if (!backendWrapper) {\n")
	b.WriteString("    backendWrapper = new WSBackend(url);\n")
	fmt.Fprintf(&b, "    backendInstance = backendWrapper.api as unknown as %s;\n", clientName)
	b.WriteString("  }\n")
	b.WriteString("  return { api: backendInstance!, backend: backendWrapper! };\n")
	b.WriteString("}\n")

	return b.String(), nil
}

func collectStructs(t reflect.Type, m map[string]reflect.Type) {
	if t == nil {
		return
	}
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		name := t.Name()
		if name == "" {
			return
		}
		if _, ok := m[name]; ok {
			return
		}
		m[name] = t

		for field := range t.Fields() {
			collectStructs(field.Type, m)
		}

	case reflect.Slice, reflect.Array:
		collectStructs(t.Elem(), m)
	case reflect.Chan:
		collectStructs(t.Elem(), m)
	case reflect.Map:
		collectStructs(t.Elem(), m)
	}
}

func goTypeToTS(t reflect.Type) string {
	if t == nil {
		return "any"
	}
	if t.Kind() == reflect.Pointer {
		return goTypeToTS(t.Elem())
	}
	switch t.Kind() {
	case reflect.Chan:
		return goTypeToTS(t.Elem())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Float32, reflect.Float64:
		return "number"
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Struct:
		if t.PkgPath() == "time" && t.Name() == "Time" {
			return "Date"
		}

		if t.Name() != "" {
			return t.Name()
		}
		var parts []string
		for f := range t.Fields() {
			fn := f.Name
			tag := f.Tag.Get("json")
			if tag != "" {
				parts = append(parts, fmt.Sprintf("%s: %s", strings.Split(tag, ",")[0], goTypeToTS(f.Type)))
			} else {
				parts = append(parts, fmt.Sprintf("%s: %s", fn, goTypeToTS(f.Type)))
			}
		}
		return fmt.Sprintf("{ %s }", strings.Join(parts, "; "))
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			return "string"
		}
		return goTypeToTS(t.Elem()) + "[]"
	case reflect.Map:
		key := t.Key()
		if key.Kind() == reflect.String {
			return fmt.Sprintf("{ [key: string]: %s }", goTypeToTS(t.Elem()))
		}
		return "any"
	default:
		return "any"
	}
}
