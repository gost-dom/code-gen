package wrappers

import (
	"fmt"
	"iter"
	"log/slog"
	"strings"
	"unicode"

	g "github.com/gost-dom/generators"
	"github.com/gost-dom/webref/idl"

	"github.com/dave/jennifer/jen"
)

var (
	v8FunctionTemplatePtr     = g.NewTypePackage("FunctionTemplate", v8).Pointer()
	v8FunctionCallbackInfoPtr = g.NewTypePackage("FunctionCallbackInfo", v8).Pointer()
	v8Value                   = g.NewTypePackage("Value", v8).Pointer()
	v8ReadOnly                = g.Raw(jen.Qual(v8, "ReadOnly"))
	v8None                    = g.Raw(jen.Qual(v8, "None"))
	scriptHostPtr             = g.NewType("V8ScriptHost").Pointer()
)

const (
	dom      = BASE_PKG + "/dom"
	html     = BASE_PKG + "/html"
	v8host   = BASE_PKG + "/scripting/v8host"
	gojahost = BASE_PKG + "/scripting/gojahost"
	log      = BASE_PKG + "/internal/log"
	v8       = "github.com/tommie/v8go"
	gojaSrc  = "github.com/dop251/goja"
)

func createData(spec idl.Spec, dataData WrapperTypeSpec) ESConstructorData {
	idlName, ok := spec.GetType(dataData.TypeName)
	if !ok {
		panic("Missing type")
	}
	wrappedTypeName := dataData.InnerTypeName
	if wrappedTypeName == "" {
		wrappedTypeName = idlName.IdlInterface.Name
	}
	wrapperTypeBaseName := dataData.WrapperTypeName
	if wrapperTypeBaseName == "" {
		wrapperTypeBaseName = fmt.Sprintf("%sV8Wrapper", wrappedTypeName)
	}
	return ESConstructorData{
		Spec:                dataData,
		InnerTypeName:       wrappedTypeName,
		WrapperTypeName:     lowerCaseFirstLetter(wrapperTypeBaseName),
		WrapperTypeBaseName: wrapperTypeBaseName,
		Receiver:            dataData.Receiver,
		RunCustomCode:       dataData.RunCustomCode,
		Inheritance:         idlName.Inheritance(),
		Constructor:         CreateConstructor(dataData, idlName),
		Operations:          CreateInstanceMethods(dataData, idlName),
		Attributes:          CreateAttributes(dataData, idlName),
	}
}

func CreateConstructor(
	dataData WrapperTypeSpec,
	idlName idl.TypeSpec) *ESOperation {
	if c, ok := idlName.Constructor(); ok {
		fmt.Printf("Create constructor %s '%s'\n", dataData.TypeName, c.Name)
		c.Name = "constructor"
		result := createOperation(dataData, c)
		return &result
	} else {
		return nil
	}
}

func CreateInstanceMethods(
	dataData WrapperTypeSpec,
	idlName idl.TypeSpec) (result []ESOperation) {
	for instanceMethod := range idlName.InstanceMethods() {
		op := createOperation(dataData, instanceMethod)
		result = append(result, op)
	}
	return
}

func CreateAttributes(
	dataData WrapperTypeSpec,
	idlName idl.TypeSpec,
) (res []ESAttribute) {
	for attribute := range idlName.IdlInterface.AllAttributes(dataData.IncludeIncludes) {
		methodCustomization := dataData.GetMethodCustomization(attribute.Name)
		if methodCustomization.Ignored || attribute.Type.Name == "EventHandler" {
			continue
		}
		var (
			getter *ESOperation
			setter *ESOperation
		)
		// r := attribute.AttributeType()
		// rtnType := r.TypeName
		getter = &ESOperation{
			Name:                 attribute.Name,
			NotImplemented:       methodCustomization.NotImplemented,
			CustomImplementation: methodCustomization.CustomImplementation,
			RetType: idl.RetType{
				TypeName: attribute.Type.Name,
				Nullable: attribute.Type.Nullable,
			},
			MethodCustomization: methodCustomization,
		}
		if !attribute.Readonly {
			setter = new(ESOperation)
			*setter = *getter
			setter.Name = fmt.Sprintf("set%s", idlNameToGoName(getter.Name))
			methodCustomization := dataData.GetMethodCustomization(setter.Name)
			setter.NotImplemented = setter.NotImplemented || methodCustomization.NotImplemented
			setter.CustomImplementation = setter.CustomImplementation ||
				methodCustomization.CustomImplementation
			setter.RetType = idl.NewRetTypeUndefined()
			setter.Arguments = []ESOperationArgument{{
				Name:     "val",
				Type:     idlNameToGoName(attribute.Type.Name),
				Optional: false,
				Variadic: false,
			}}
		}
		getterCustomization := dataData.GetMethodCustomization(getter.Name)
		getter.NotImplemented = getterCustomization.NotImplemented || getter.NotImplemented
		getter.CustomImplementation = getterCustomization.CustomImplementation ||
			getter.CustomImplementation
		res = append(res, ESAttribute{attribute.Name, getter, setter})
	}
	return
}

func createOperation(typeSpec WrapperTypeSpec, member idl.MemberSpec) ESOperation {
	methodCustomization := typeSpec.GetMethodCustomization(member.Name)
	op := ESOperation{
		Name:                 member.Name,
		NotImplemented:       methodCustomization.NotImplemented,
		CustomImplementation: methodCustomization.CustomImplementation,
		RetType:              member.ReturnType(),
		MethodCustomization:  methodCustomization,
		HasError:             !methodCustomization.HasNoError,
		Arguments:            []ESOperationArgument{},
	}
	for _, arg := range member.Arguments {
		var esArgumentSpec ESMethodArgument
		if arg := methodCustomization.Argument(arg.Name); arg != nil {
			esArgumentSpec = *arg
		}
		esArg := ESOperationArgument{
			Name:         arg.Name,
			Optional:     arg.Optional && !esArgumentSpec.required,
			IdlType:      arg.IdlType,
			ArgumentSpec: esArgumentSpec,
			Ignore:       esArgumentSpec.ignored,
		}
		if len(arg.IdlType.Types) > 0 {
			slog.Warn(
				"Multiple argument types",
				"Operation",
				member.Name,
				"Argument",
				arg.Name,
			)
		}
		if arg.IdlType.IdlType != nil {
			esArg.Type = arg.IdlType.IdlType.IType.TypeName
		}
		op.Arguments = append(op.Arguments, esArg)
	}
	return op
}

type ESOperationArgument struct {
	Name         string
	Type         string
	Optional     bool
	Variadic     bool
	IdlType      idl.IdlTypes
	ArgumentSpec ESMethodArgument
	Ignore       bool
}

func (a ESOperationArgument) OptionalInGo() bool {
	hasDefault := a.ArgumentSpec.hasDefault
	return a.Optional && !hasDefault
}

func (a ESOperationArgument) DefaultValueInGo() (name string, ok bool) {
	ok = a.Optional && a.ArgumentSpec.hasDefault
	if defaultValue := a.ArgumentSpec.defaultValue; defaultValue != "" {
		name = defaultValue
	} else {
		name = fmt.Sprintf("default%s", a.Type)
	}
	return
}

type ESOperation struct {
	Name                 string
	NotImplemented       bool
	RetType              idl.RetType
	HasError             bool
	CustomImplementation bool
	MethodCustomization  ESMethodWrapper
	Arguments            []ESOperationArgument
}

func (o ESOperation) WrapperMethodName() string {
	return idl.SanitizeName(o.Name)
}

func (op ESOperation) GetHasError() bool {
	return op.HasError
}

func (op ESOperation) HasResult() bool {
	return op.RetType.IsDefined()
}

func (o ESOperation) Encoder() string {
	if e := o.MethodCustomization.Encoder; e != "" {
		return e
	}
	converter := "to"
	if o.RetType.Nullable {
		converter += "Nullable"
	}
	converter += idlNameToGoName(o.RetType.TypeName)
	return converter
}

type ESAttribute struct {
	Name   string
	Getter *ESOperation
	Setter *ESOperation
}

type ESConstructorData struct {
	Spec                *ESClassWrapper
	InnerTypeName       string
	WrapperTypeName     string
	WrapperTypeBaseName string
	Receiver            string
	Inheritance         string
	Operations          []ESOperation
	Attributes          []ESAttribute
	Constructor         *ESOperation
	RunCustomCode       bool
}

func (d ESConstructorData) GetInternalPackage() string {
	switch d.Spec.DomSpec.Name {
	case "dom":
		return dom
	case "html":
		return html
	default:
		return html
	}
}

func (d ESConstructorData) WrapperFunctionsToInstall() iter.Seq[ESOperation] {
	return func(yield func(ESOperation) bool) {
		for _, op := range d.Operations {
			if !op.MethodCustomization.Ignored && !yield(op) {
				return
			}
		}
	}
}

func (d ESConstructorData) AttributesToInstall() iter.Seq[ESAttribute] {
	return func(yield func(ESAttribute) bool) {
		for _, a := range d.Attributes {
			if !yield(a) {
				return
			}
		}
	}
}

func (d ESConstructorData) WrapperFunctionsToGenerate() iter.Seq[ESOperation] {
	return func(yield func(ESOperation) bool) {
		for op := range d.WrapperFunctionsToInstall() {
			if !op.MethodCustomization.CustomImplementation && !yield(op) {
				return
			}
		}
		for _, a := range d.Attributes {
			if a.Getter != nil && !a.Getter.CustomImplementation {
				yield(*a.Getter)
			}
			if a.Setter != nil && !a.Setter.CustomImplementation {
				yield(*a.Setter)
			}
		}
	}
}

func (d ESConstructorData) Name() string { return d.Spec.TypeName }

func ReturnOnAnyError(errNames []g.Generator) g.Generator {
	if len(errNames) == 0 {
		return g.Noop
	}
	if len(errNames) == 1 {
		return ReturnOnError{err: errNames[0]}
	}
	return g.StatementList(
		g.Assign(g.Id("err"),
			g.Raw(jen.Qual("errors", "Join").CallFunc(func(g *jen.Group) {
				for _, e := range errNames {
					g.Add(e.Generate())
				}
			})),
		),
		ReturnOnError{},
	)
}

type JenGenerator = g.Generator

func IsNodeType(typeName string) bool {
	loweredName := strings.ToLower(typeName)
	switch loweredName {
	case "node":
		return true
	case "document":
		return true
	case "documentfragment":
		return true
	}
	if strings.HasSuffix(loweredName, "element") {
		return true
	}
	return false
}

// sanitizeVarName create a valid go variable name from a variable to avoid
// invalid generated code due to
//
//   - The name is a reserved word, e.g. `type`.
//   - The name already an identifiers in scope (not yet implemented)
func sanitizeVarName(name string) string {
	switch name {
	case "type":
		return "type_"
	case "select":
		return "select_"
	}
	return name
}

func idlNameToGoName(s string) string {
	words := strings.Split(s, " ")
	for i, word := range words {
		words[i] = upperCaseFirstLetter(word)
	}
	return strings.Join(words, "")
}

func idlNameToUnexportedGoName(s string) string {
	return lowerCaseFirstLetter(idlNameToGoName(s))
}

func lowerCaseFirstLetter(s string) string {
	strLen := len(s)
	if strLen == 0 {
		slog.Warn("Passing empty string to upperCaseFirstLetter")
		return ""
	}
	buffer := make([]rune, 0, strLen)
	buffer = append(buffer, unicode.ToLower([]rune(s)[0]))
	buffer = append(buffer, []rune(s)[1:]...)
	return string(buffer)
}
func upperCaseFirstLetter(s string) string {
	strLen := len(s)
	if strLen == 0 {
		slog.Warn("Passing empty string to upperCaseFirstLetter")
		return ""
	}
	buffer := make([]rune, 0, strLen)
	buffer = append(buffer, unicode.ToUpper([]rune(s)[0]))
	buffer = append(buffer, []rune(s)[1:]...)
	return string(buffer)
}

type ReturnOnError struct {
	err g.Generator
}

func (ret ReturnOnError) Generate() *jen.Statement {
	err := ret.err
	if err == nil {
		err = g.Id("err")
	}
	return g.IfStmt{
		Condition: g.Neq{Lhs: err, Rhs: g.Nil}, //g.Raw(err.Generate().Op("!=").Nil()),
		Block:     g.Return(g.Nil, err),
	}.Generate()
}
