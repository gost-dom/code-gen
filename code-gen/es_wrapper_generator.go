package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"unicode"

	g "github.com/stroiman/go-dom/code-gen/generators"

	"github.com/dave/jennifer/jen"
)

var (
	v8FunctionTemplatePtr     = g.NewTypePackage("FunctionTemplate", v8).Pointer()
	v8FunctionCallbackInfoPtr = g.NewTypePackage("FunctionCallbackInfo", v8).Pointer()
	v8Value                   = g.NewTypePackage("Value", v8).Pointer()
	scriptHostPtr             = g.NewType("ScriptHost").Pointer()
)

func createData(data []byte, dataData ESClassWrapper) (ESConstructorData, error) {
	spec := ParsedIdlFile{}
	var constructor *ESOperation
	err := json.Unmarshal(data, &spec)
	if err != nil {
		panic(err)
	}
	idlName := spec.IdlNames[dataData.TypeName]
	type tmp struct {
		Op ESOperation
		Ok bool
	}
	ops := []*tmp{}
	attributes := []ESAttribute{}
	for _, member := range idlName.Members {
		if member.Special == "static" {
			continue
		}
		if -1 != slices.IndexFunc(
			ops,
			func(op *tmp) bool { return op.Op.Name == member.Name },
		) {
			slog.Warn("Function overloads", "Name", member.Name)
			continue
		}
		returnType, nullable := FindMemberReturnType(member)
		methodCustomization := dataData.GetMethodCustomization(member.Name)
		operation := &tmp{ESOperation{
			Name:                 member.Name,
			NotImplemented:       methodCustomization.NotImplemented,
			CustomImplementation: methodCustomization.CustomImplementation,
			ReturnType:           returnType,
			Nullable:             nullable,
			MethodCustomization:  methodCustomization,
			Arguments:            []ESOperationArgument{},
		}, true}
		if member.Type == "operation" {
			operation.Op.HasError = !operation.Op.MethodCustomization.HasNoError
			ops = append(ops, operation)
		}
		for _, arg := range member.Arguments {
			esArg := ESOperationArgument{
				Name:     arg.Name,
				Optional: arg.Optional,
				IdlType:  arg.IdlType,
			}
			if len(arg.IdlType.Types) > 0 {
				slog.Warn(
					"Multiple argument types",
					"Operation",
					member.Name,
					"Argument",
					arg.Name,
				)
				operation.Ok = false
				break
			}
			if arg.IdlType.IdlType != nil {
				esArg.Type = arg.IdlType.IdlType.IType.TypeName
			}
			operation.Op.Arguments = append(operation.Op.Arguments, esArg)
		}
		if member.Type == "constructor" {
			constructor = &operation.Op
		}
		if IsAttribute(member) {
			op := operation.Op
			var (
				getter *ESOperation
				setter *ESOperation
			)
			rtnType, nullable := FindMemberAttributeType(member)
			getter = new(ESOperation)
			*getter = op
			getterName := idlNameToGoName(op.Name)
			if !member.Readonly {
				getterName = fmt.Sprintf("Get%s", getterName)
			}
			getter.Name = getterName
			getter.ReturnType = rtnType
			getter.Nullable = nullable
			getterCustomization := dataData.GetMethodCustomization(getter.Name)
			getter.NotImplemented = getterCustomization.NotImplemented || op.NotImplemented
			getter.CustomImplementation = getterCustomization.CustomImplementation ||
				op.CustomImplementation
			if !member.Readonly {
				setter = new(ESOperation)
				*setter = op
				setter.Name = fmt.Sprintf("Set%s", idlNameToGoName(op.Name))
				methodCustomization := dataData.GetMethodCustomization(setter.Name)
				setter.NotImplemented = methodCustomization.NotImplemented ||
					op.NotImplemented
				setter.CustomImplementation = methodCustomization.CustomImplementation ||
					op.CustomImplementation
				setter.ReturnType = "undefined"
				setter.Arguments = []ESOperationArgument{{
					Name:     "val",
					Type:     idlNameToGoName(rtnType),
					Optional: false,
					Variadic: false,
					// IdlType  IdlTypes
				}}
			}
			attributes = append(attributes, ESAttribute{op.Name, getter, setter})
		}
	}

	operations := make([]ESOperation, 0, len(ops))
	for _, op := range ops {
		if op.Ok {
			operations = append(operations, op.Op)
		}
	}
	wrappedTypeName := dataData.InnerTypeName
	if wrappedTypeName == "" {
		wrappedTypeName = idlName.Name
	}
	wrapperTypeName := dataData.WrapperTypeName
	if wrapperTypeName == "" {
		wrapperTypeName = "ES" + wrappedTypeName
	}
	return ESConstructorData{
		InnerTypeName:    wrappedTypeName,
		WrapperTypeName:  wrapperTypeName,
		Receiver:         dataData.Receiver,
		Operations:       operations,
		Attributes:       attributes,
		Constructor:      constructor,
		CreatesInnerType: true,
		IdlName:          idlName,
		RunCustomCode:    dataData.RunCustomCode,
	}, nil
}

const br = "github.com/stroiman/go-dom/browser/dom"
const sc = "github.com/stroiman/go-dom/browser/scripting"
const v8 = "github.com/tommie/v8go"

type ESOperationArgument struct {
	Name     string
	Type     string
	Optional bool
	Variadic bool
	IdlType  IdlTypes
}

type ESOperation struct {
	Name                 string
	NotImplemented       bool
	ReturnType           string
	Nullable             bool
	HasError             bool
	CustomImplementation bool
	MethodCustomization  ESMethodWrapper
	Arguments            []ESOperationArgument
}

func (op ESOperation) GetHasError() bool {
	return op.HasError
}

type ESAttribute struct {
	Name   string
	Getter *ESOperation
	Setter *ESOperation
}

type ESConstructorData struct {
	CreatesInnerType bool
	InnerTypeName    string
	WrapperTypeName  string
	Receiver         string
	Operations       []ESOperation
	Attributes       []ESAttribute
	Constructor      *ESOperation
	RunCustomCode    bool
	IdlName
}

type Imports = [][][2]string

func WriteImports(b *builder, imports Imports) {
	b.Printf("import (\n")
	b.indent()
	defer b.unIndentF(")\n\n")
	for _, grp := range imports {
		for _, imp := range grp {
			alias := imp[0]
			pkg := imp[1]
			if alias == "" {
				b.Printf("\"%s\"\n", pkg)
			} else {
				b.Printf("%s \"%s\"\n", imp[0], imp[1])
			}
		}
	}
}

func IllegalConstructor(data ESConstructorData) g.Generator {
	return g.Return(g.Nil,
		g.Raw(jen.Qual(v8, "NewTypeError").Call(
			jen.Id(data.Receiver).Dot("host").Dot("iso"), jen.Lit("Illegal Constructor"),
		)),
	)
}

func ReturnOnAnyError(errNames []g.Generator) g.Generator {
	if len(errNames) == 0 {
		return g.Noop
	}
	if len(errNames) == 1 {
		return GenReturnOnErrorName(errNames[0])
	}
	return Statements(
		g.Assign(g.Id("err"),
			g.Raw(jen.Qual("errors", "Join").CallFunc(func(g *jen.Group) {
				for _, e := range errNames {
					g.Add(e.Generate())
				}
			})),
		),
		GenReturnOnError(),
	)
}

func WrapperCalls(
	op ESOperation,
	baseFunctionName string,
	argNames []g.Generator,
	errorsNames []g.Generator,
	createCallInstance func(string, []g.Generator, ESOperation) g.Generator,
) g.Generator {
	arguments := op.Arguments
	statements := StatementList()
	for i := len(arguments); i >= 0; i-- {
		functionName := baseFunctionName
		for j, arg := range arguments {
			if j < i {
				if arg.Optional {
					functionName += idlNameToGoName(arg.Name)
				}
			}
		}
		argnames := argNames[0:i]
		errNames := errorsNames[0:i]
		callInstance := createCallInstance(functionName, argnames, op)
		if i > 0 {
			arg := arguments[i-1]
			statements.Append(StatementList(
				IfStmt{
					Condition: g.Raw(jen.Id("args").Dot("noOfReadArguments").Op(">=").Lit(i)),
					Block: StatementList(
						ReturnOnAnyError(errNames),
						callInstance,
					),
				}))
			if !(arg.Optional) {
				statements.Append(
					g.Return(
						g.Nil,
						g.Raw(jen.Qual("errors", "New").Call(jen.Lit("Missing arguments"))),
					),
				)
				break
			}
		} else {
			statements.Append(callInstance)
		}
	}
	return statements
}

func RequireContext(receiver string) g.Generator {
	return g.Assign(g.Id("ctx"), Stmt{jen.Id(receiver).Dot("host").Dot("MustGetContext").Call(
		jen.Id("info").Dot("Context").Call(),
	)})
}

func JSConstructorImpl(data ESConstructorData) g.Generator {
	if data.Constructor == nil {
		return IllegalConstructor(data)
	}
	var readArgsResult ReadArgumentsResult
	readArgsResult = ReadArguments(data, *data.Constructor)
	statements := StatementList(readArgsResult)
	statements.Append(RequireContext(data.Receiver))
	baseFunctionName := "CreateInstance"
	var CreateCall = func(functionName string, argnames []g.Generator, op ESOperation) g.Generator {
		return StatementList(
			g.Return(
				g.Raw(jen.Id(data.Receiver).Dot(functionName).CallFunc(func(grp *jen.Group) {
					grp.Add(jen.Id("ctx"))
					grp.Add(jen.Id("info").Dot("This").Call())
					for _, name := range argnames {
						grp.Add(name.Generate())
					}
				})),
			),
		)
	}
	statements.Append(
		WrapperCalls(
			*data.Constructor,
			baseFunctionName,
			readArgsResult.ArgNames,
			readArgsResult.ErrNames,
			CreateCall,
		),
	)
	return statements
}

func CreateInstance(typeName string, params ...jen.Code) JenGenerator {
	constructorName := fmt.Sprintf("New%s", typeName)
	return Stmt{
		jen.Id(constructorName).Call(params...),
	}
}

func Run(f *jen.File, data ESConstructorData) {
	gen := StatementList(
		CreateConstructor(data),
		CreateConstructorWrapper(data),
		CreateWrapperMethods(data),
	)
	f.Add(gen.Generate())
}

func CreateConstructor(data ESConstructorData) g.Generator {
	return g.FunctionDefinition{
		Name:     fmt.Sprintf("Create%sPrototype", data.InnerTypeName),
		Args:     g.Arg(g.Id("host"), scriptHostPtr),
		RtnTypes: g.List(v8FunctionTemplatePtr),
		Body:     CreateConstructorBody(data),
	}
}

func CreateConstructorBody(data ESConstructorData) g.Generator {
	scriptHost := g.NewValue("host")
	wrapper := g.NewValue("wrapper")
	constructor := g.NewValue("constructor")
	instanceTemplate := constructor.Method("GetInstanceTemplate").Call()
	SetInternalFieldCount := instanceTemplate.Method("SetInternalFieldCount")

	statements := StatementList(
		g.Assign(g.Id("iso"), scriptHost.Field("iso")),
		g.Assign(
			wrapper,
			CreateInstance(data.WrapperTypeName, jen.Id("host")),
		),
		g.Assign(constructor, NewFunctionTemplate{g.Id("iso"), wrapper.Method("NewInstance")}),
		SetInternalFieldCount.Call(g.Lit(1)),
		g.Assign(
			g.Id("prototype"),
			constructor.Method("PrototypeTemplate").Call(),
		),
		NewLine(),
		InstallFunctionHandlers(data),
		InstallAttributeHandlers(data),
	)
	if data.RunCustomCode {
		statements.Append(
			g.Raw(jen.Id("wrapper").Dot("CustomInitialiser").Call(jen.Id("constructor"))),
		)
	}
	statements.Append(g.Return(constructor))
	return statements
}

func InstallFunctionHandlers(data ESConstructorData) JenGenerator {
	generators := make([]JenGenerator, len(data.Operations))
	for i, op := range data.Operations {
		generators[i] = InstallFunctionHandler(op)
	}
	return StatementList(generators...)
}

func InstallFunctionHandler(op ESOperation) JenGenerator {
	f := jen.Id("wrapper").Dot(idlNameToGoName(op.Name))
	ft := NewFunctionTemplate{g.Id("iso"), Stmt{f}}
	return Stmt{(jen.Id("prototype").Dot("Set").Call(jen.Lit(op.Name), ft.Generate()))}
}

func InstallAttributeHandlers(data ESConstructorData) g.Generator {
	length := len(data.Attributes)
	if length == 0 {
		return g.Noop
	}
	generators := make([]JenGenerator, length+1)
	generators[0] = g.Line
	for i, op := range data.Attributes {
		generators[i+1] = InstallAttributeHandler(op)
	}
	return StatementList(generators...)
}

func InstallAttributeHandler(op ESAttribute) g.Generator {
	getter := op.Getter
	setter := op.Setter
	list := StatementList()
	if getter != nil {
		f := jen.Id("wrapper").Dot(idlNameToGoName(getter.Name))
		ft := NewFunctionTemplate{g.Id("iso"), Stmt{f}}
		var setterFt g.Generator
		var Attributes = "ReadOnly"
		if setter != nil {
			f := Stmt{jen.Id("wrapper").Dot(idlNameToGoName(setter.Name))}
			setterFt = NewFunctionTemplate{g.Id("iso"), f}
			Attributes = "None"
		} else {
			setterFt = g.Nil
		}

		list.Append(Stmt{
			(jen.Id("prototype").Dot("SetAccessorProperty").Call(jen.Lit(op.Name), jen.Line().Add(ft.Generate()), jen.Line().Add(setterFt.Generate()), jen.Line().Add(jen.Qual(v8, Attributes)))),
		})
	}
	return list
}

type JenGenerator = g.Generator

type GetArgStmt struct {
	Name     string
	Receiver string
	ErrName  string
	Getter   string
	Index    int
	Arg      ESOperationArgument
}

type IfStmt struct {
	Condition JenGenerator
	Block     JenGenerator
	Else      JenGenerator
}

func (s GetArgStmt) Generate() *jen.Statement {
	if s.Arg.Type != "" {
		return AssignmentStmt{
			VarNames: []string{s.Name, s.ErrName},
			Expression: Stmt{
				jen.Id(s.Receiver).Dot(s.Getter).Call(jen.Id("args"), jen.Lit(s.Index)),
			},
		}.Generate()
	} else {
		statements := []jen.Code{jen.Id("ctx"), jen.Id("args"), jen.Lit(s.Index)}
		for _, t := range s.Arg.IdlType.IdlType.IType.Types {
			parserName := fmt.Sprintf("Get%sFrom%s", idlNameToGoName(s.Arg.Name), t.IType.TypeName)
			statements = append(statements, jen.Id(parserName))
		}
		return AssignmentStmt{
			VarNames:   []string{s.Name, s.ErrName},
			Expression: Stmt{jen.Id("TryParseArgs").Call(statements...)},
		}.Generate()
	}
}

type AssignmentStmt struct {
	VarNames   []string
	Expression JenGenerator
	NoNewVars  bool
}

type StatementListStmt struct {
	Statements []JenGenerator
}

func StatementList(statements ...JenGenerator) StatementListStmt {
	return StatementListStmt{statements}
}

func NewLine() JenGenerator { return Stmt{jen.Line()} }

func (s AssignmentStmt) Generate() *jen.Statement {
	list := make([]jen.Code, 0, len(s.VarNames))
	for _, n := range s.VarNames {
		list = append(list, jen.Id(n))
	}
	operator := ":="
	if s.NoNewVars {
		operator = "="
	}
	return jen.List(list...).Op(operator).Add(s.Expression.Generate())
}

func (s *StatementListStmt) Prepend(stmt JenGenerator) {
	s.Statements = slices.Insert(s.Statements, 0, stmt)
}

func (s IfStmt) Generate() *jen.Statement {
	result := jen.If(s.Condition.Generate()).Block(s.Block.Generate())
	if s.Else != nil {
		result.Else().Block(s.Else.Generate())
	}
	return result
}

func GetSliceLength(gen JenGenerator) JenGenerator {
	return Stmt{jen.Len(gen.Generate())}
}

func Statements(stmts ...JenGenerator) JenGenerator {
	return StatementListStmt{stmts}
}

func (s *StatementListStmt) Append(stmt ...JenGenerator) {
	s.Statements = append(s.Statements, stmt...)
}
func (s *StatementListStmt) AppendJen(stmt *jen.Statement) {
	s.Statements = append(s.Statements, Stmt{stmt})
}

func (s StatementListStmt) Generate() *jen.Statement {
	result := []jen.Code{}
	for _, s := range s.Statements {
		jenStatement := s.Generate()
		if jenStatement != nil && len(*jenStatement) != 0 {
			if len(result) != 0 {
				result = append(result, jen.Line())
			}
			result = append(result, jenStatement)
		}
	}
	jenStmt := jen.Statement(result)
	return &jenStmt
}

type CallInstance struct {
	Name     string
	Args     []g.Generator
	Op       ESOperation
	Instance g.Generator
}

type GetGeneratorResult struct {
	Generator      JenGenerator
	HasValue       bool
	HasError       bool
	RequireContext bool
}

func (c CallInstance) PerformCall(instanceName string) (genRes GetGeneratorResult) {
	args := []g.Generator{}
	genRes.HasError = c.Op.GetHasError()
	genRes.HasValue = c.Op.ReturnType != "undefined"
	var stmt *jen.Statement
	if genRes.HasValue {
		stmt = jen.Id("result")
	}
	if genRes.HasError {
		if stmt != nil {
			stmt = stmt.Op(",").Id("err")
		} else {
			stmt = jen.Id("err")
		}
	}
	if stmt != nil {
		if genRes.HasValue {
			stmt = stmt.Op(":=")
		} else {
			stmt = stmt.Op("=")
		}
	}

	for _, a := range c.Args {
		args = append(args, a)
	}
	list := StatementListStmt{}
	var evaluation g.Generator
	if c.Instance == nil {
		evaluation = g.NewValue(idlNameToGoName(c.Name)).Call(args...)
	} else {
		evaluation = g.NewValue(instanceName).Method(idlNameToGoName(c.Name)).Call(args...)
	}
	if stmt == nil {
		list.Append(evaluation)
	} else {
		list.Append(g.Raw(stmt.Add(evaluation.Generate())))
	}
	genRes.Generator = list
	return
}

func (c CallInstance) GetGenerator(receiver string, instanceName string) GetGeneratorResult {
	genRes := c.PerformCall(instanceName)
	list := StatementListStmt{}
	list.Append(genRes.Generator)
	if !genRes.HasValue {
		if genRes.HasError {
			list.Append(Stmt{jen.Return(jen.Nil(), jen.Id("err"))})
		} else {
			list.Append(Stmt{jen.Return(jen.Nil(), jen.Nil())})
		}
	} else {
		converter := "To"
		if c.Op.Nullable {
			converter += "Nullable"
		}
		converter += idlNameToGoName(c.Op.ReturnType)
		genRes.RequireContext = true
		valueReturn := Stmt{jen.Return(jen.Id(receiver).Dot(converter).Call(jen.Id("ctx"), jen.Id("result")))}
		if genRes.HasError {
			list.Append(IfStmt{
				Condition: Stmt{jen.Id("err").Op("!=").Nil()},
				Block:     Stmt{jen.Return(jen.Nil(), jen.Id("err"))},
				Else:      valueReturn,
			})
		} else {
			list.Append(valueReturn)
		}
	}
	genRes.Generator = list
	return genRes
}

type ReadArgumentsResult struct {
	ArgNames  []g.Generator
	ErrNames  []g.Generator
	Generator g.Generator
}

func (r ReadArgumentsResult) Generate() *jen.Statement {
	if r.Generator != nil {
		return r.Generator.Generate()
	} else {
		return g.Noop.Generate()
	}
}

func ReadArguments(data ESConstructorData, op ESOperation) (res ReadArgumentsResult) {
	argCount := len(op.Arguments)
	res.ArgNames = make([]g.Generator, argCount)
	res.ErrNames = make([]g.Generator, argCount)
	statements := &StatementListStmt{}
	if argCount > 0 {
		statements.Append(
			g.Assign(
				g.Id("args"),
				g.Raw(
					jen.Id("newArgumentHelper").
						Call(jen.Id(data.Receiver).Dot("host"), jen.Id("info")),
				),
			),
		)
	}
	for i, arg := range op.Arguments {
		argName := g.Id(arg.Name)
		errName := g.Id(fmt.Sprintf("err%d", i))
		if len(op.Arguments) == 1 {
			errName = g.Id("err")
		}
		res.ArgNames[i] = argName
		res.ErrNames[i] = errName

		var convertNames []string
		if arg.Type != "" {
			convertNames = []string{fmt.Sprintf("Decode%s", idlNameToGoName(arg.Type))}
		} else {
			types := arg.IdlType.IdlType.IType.Types
			convertNames = make([]string, len(types))
			for i, t := range types {
				convertNames[i] = fmt.Sprintf("Decode%s", t.IType.TypeName)
			}
		}

		converters := make([]jen.Code, 0)
		converters = append(converters, jen.Id("args"))
		converters = append(converters, jen.Lit(i))
		for _, n := range convertNames {
			converters = append(converters, g.Raw(jen.Id(data.Receiver).Dot(n)).Generate())
		}
		statements.Append(g.Assign(
			g.Raw(jen.List(argName.Generate(), errName.Generate())),
			Stmt{jen.Id("TryParseArg").Call(converters...)}))
	}
	res.Generator = statements
	return
}

func GetInstanceAndError(id g.Generator, data ESConstructorData) g.Generator {
	return StatementList(
		g.AssignMany(
			g.List(id, g.Id("err")),
			g.Raw(jen.Id(data.Receiver).Dot("GetInstance").Call(jen.Id("info"))),
		),
		GenReturnOnError(),
	)
}

func FunctionTemplateCallbackBody(
	data ESConstructorData,
	op ESOperation,
) JenGenerator {
	if op.NotImplemented {
		errMsg := fmt.Sprintf("Not implemented: %s.%s", data.Name, op.Name)
		return g.Return(g.Nil, g.Raw(jen.Qual("errors", "New").Call(jen.Lit(errMsg))))
	}
	instance := g.Id("instance")
	readArgsResult := ReadArguments(data, op)
	statements := StatementList(GetInstanceAndError(instance, data), readArgsResult)
	requireContext := false
	var CreateCall = func(functionName string, argnames []g.Generator, op ESOperation) g.Generator {
		callInstance := CallInstance{
			Name:     functionName,
			Args:     argnames,
			Op:       op,
			Instance: instance,
		}.GetGenerator(data.Receiver, "instance")
		requireContext = requireContext || callInstance.RequireContext
		return callInstance.Generator
	}
	statements.Append(
		WrapperCalls(
			op,
			idlNameToGoName(op.Name),
			readArgsResult.ArgNames,
			readArgsResult.ErrNames,
			CreateCall,
		),
	)
	if requireContext {
		statements.Prepend(RequireContext(data.Receiver))
	}
	return statements
}

func CreateConstructorWrapper(data ESConstructorData) JenGenerator {
	return StatementList(
		g.Line,
		g.FunctionDefinition{
			Name: "NewInstance",
			Receiver: g.FunctionArgument{
				Name: g.Id(data.Receiver),
				Type: g.Id(data.WrapperTypeName),
			},
			Args:     g.Arg(g.Id("info"), v8FunctionCallbackInfoPtr),
			RtnTypes: g.List(v8Value, g.Id("error")),
			Body:     JSConstructorImpl(data),
		},
	)
}

func CreateWrapperMethods(data ESConstructorData) JenGenerator {
	generators := make([]JenGenerator, 0, len(data.Operations))
	for _, op := range data.Operations {
		generators = append(generators, CreateWrapperMethod(data, op))
	}
	list := StatementList(generators...)
	for _, attr := range data.Attributes {
		if attr.Getter != nil {
			list.Append(CreateWrapperMethod(data, *attr.Getter))
		}
		if attr.Setter != nil {
			list.Append(CreateWrapperMethod(data, *attr.Setter))
		}
	}
	return list
}

func CreateWrapperMethod(
	data ESConstructorData,
	op ESOperation,
) JenGenerator {
	if op.CustomImplementation {
		return g.Noop
	}
	return StatementList(
		NewLine(),
		g.FunctionDefinition{
			Receiver: g.FunctionArgument{
				Name: g.Id(data.Receiver),
				Type: g.Id(data.WrapperTypeName),
			},
			Name:     idlNameToGoName(op.Name),
			Args:     g.Arg(g.Id("info"), v8FunctionCallbackInfoPtr),
			RtnTypes: g.List(v8Value, g.Id("error")),
			Body:     FunctionTemplateCallbackBody(data, op),
		})
}

type NewFunctionTemplate struct {
	iso JenGenerator
	f   JenGenerator
}

func (t NewFunctionTemplate) Generate() *jen.Statement {
	return jen.Qual(v8, "NewFunctionTemplateWithError").Call(t.iso.Generate(), t.f.Generate())
}

func idlNameToGoName(s string) string {
	words := strings.Split(s, " ")
	for i, word := range words {
		words[i] = upperCaseFirstLetter(word)
	}
	return strings.Join(words, "")
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

type Stmt struct{ *jen.Statement }

func (s Stmt) Generate() *jen.Statement { return s.Statement }

func GenReturnOnErrorName(name g.Generator) JenGenerator {
	stmt := IfStmt{
		Condition: g.Raw(name.Generate().Op("!=").Nil()),
		Block:     g.Return(g.Raw(jen.Nil()), name),
	}
	return stmt
}

func GenReturnOnError() JenGenerator {
	return GenReturnOnErrorName(g.Id("err"))
}

func WriteReturnOnError(grp *jen.Group) {
	grp.Add(GenReturnOnError().Generate())
}

func genErrorHandler(count int) JenGenerator {
	if count == 0 {
		return StatementListStmt{}
	}
	jErr := jen.Id("err")
	result := StatementListStmt{}
	if count > 1 {
		var args []jen.Code
		for i := 0; i < count; i++ {
			args = append(args, jen.Id(fmt.Sprintf("err%d", i)))
		}
		s := Stmt{jErr.Clone().Op("=").Qual("errors", "Join").Call(args...)}
		result.Append(s)
	}
	result.Append(GenReturnOnError())
	return result

}

func WriteErrorHandler(grp *jen.Group, count int) {
	grp.Add(genErrorHandler(count).Generate())
}

func writeFactory(f *jen.File, data ESConstructorData) {
	Run(f, data)
}

type Helper struct{ *jen.Group }

func (h Helper) BuildInstance() *jen.Statement {
	return h.scriptContext().Dot("Window").Call().Dot("NewXmlHttpRequest").Call()
}

func (h Helper) v8FunctionCallbackInfoPtr() *jen.Statement {
	return h.Op("*").Qual(v8, "FunctionCallbackInfo")
}
func (h Helper) hostArg() *jen.Statement {
	return h.Id("info")
}
func (h Helper) infoArg() *jen.Statement {
	return h.Id("info")
}
func (h Helper) scriptContext() *jen.Statement {
	return h.Id("scriptContext")
}
