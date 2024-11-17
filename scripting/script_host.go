package scripting

import (
	"net/http"
	"runtime"

	. "github.com/stroiman/go-dom/browser"

	v8 "github.com/tommie/v8go"
)

type ScriptHost struct {
	iso            *v8.Isolate
	window         *v8.FunctionTemplate
	windowTemplate *v8.ObjectTemplate
	document       *v8.FunctionTemplate
	node           *v8.FunctionTemplate
	element        *v8.FunctionTemplate
	htmlElement    *v8.FunctionTemplate
	customEvent    *v8.FunctionTemplate
	eventTarget    *v8.FunctionTemplate
	contexts       map[*v8.Context]*ScriptContext
}

func (h *ScriptHost) GetContext(v8ctx *v8.Context) (*ScriptContext, bool) {
	ctx, ok := h.contexts[v8ctx]
	return ctx, ok
}

func (h *ScriptHost) MustGetContext(v8ctx *v8.Context) *ScriptContext {
	if ctx, ok := h.GetContext(v8ctx); ok {
		return ctx
	}
	panic("Unknown v8 context")
}

type ScriptContext struct {
	host     *ScriptHost
	v8ctx    *v8.Context
	window   Window
	pinner   runtime.Pinner
	v8nodes  map[ObjectId]*v8.Value
	domNodes map[ObjectId]Entity
}

func (c *ScriptContext) CacheNode(obj *v8.Object, node Entity) (*v8.Value, error) {
	val := obj.Value
	objectId := node.ObjectId()
	c.v8nodes[objectId] = val
	c.domNodes[objectId] = node
	internal, err := v8.NewValue(c.host.iso, objectId)
	if err != nil {
		return nil, err
	}
	obj.SetInternalField(0, internal)
	return val, nil
}

func (c *ScriptContext) GetInstanceForNode(
	prototype *v8.FunctionTemplate,
	node Node,
) (*v8.Value, error) {
	iso := c.host.iso
	if node == nil {
		return v8.Null(iso), nil
	}
	value, err := prototype.GetInstanceTemplate().NewInstance(c.v8ctx)
	if err == nil {
		objectId := node.ObjectId()
		if cached, ok := c.v8nodes[objectId]; ok {
			return cached, nil
		}
		return c.CacheNode(value, node)
	}
	return nil, err
}

func (c *ScriptContext) GetCachedNode(this *v8.Object) (Entity, bool) {
	result, ok := c.domNodes[this.GetInternalField(0).Int32()]
	return result, ok
}

func NewScriptHost() *ScriptHost {
	host := &ScriptHost{iso: v8.NewIsolate()}
	host.document = CreateDocumentPrototype(host)
	host.node = CreateNode(host.iso)
	host.customEvent = CreateCustomEvent(host)
	host.eventTarget = CreateEventTarget(host)
	host.element = CreateElement(host)
	host.htmlElement = CreateHtmlElement(host)
	host.window = CreateWindowTemplate(host)
	host.window.Inherit(host.eventTarget)
	host.windowTemplate = host.window.GetInstanceTemplate()
	host.element.Inherit(host.node)
	host.htmlElement.Inherit(host.element)
	host.document.Inherit(host.node)
	host.node.Inherit(host.eventTarget)
	host.contexts = make(map[*v8.Context]*ScriptContext)
	return host
}

func (host *ScriptHost) Dispose() {
	host.iso.Dispose()
}

var global *v8.Object

func (host *ScriptHost) NewContext() *ScriptContext {
	window := NewWindow()
	context := &ScriptContext{
		host:     host,
		v8ctx:    v8.NewContext(host.iso, host.windowTemplate),
		window:   window,
		v8nodes:  make(map[ObjectId]*v8.Value),
		domNodes: make(map[ObjectId]Entity),
	}
	global = context.v8ctx.Global()
	host.contexts[context.v8ctx] = context
	context.CacheNode(global, window)

	return context
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func (host *ScriptHost) createPrototypeChains() {
	host.document.Inherit(host.node)
}

func (ctx *ScriptContext) Dispose() {
	delete(ctx.host.contexts, ctx.v8ctx)
	ctx.v8ctx.Close()
}

func (ctx *ScriptContext) RunScript(script string) (*v8.Value, error) {
	return ctx.v8ctx.RunScript(script, "")
}

func (ctx *ScriptContext) Run(script string) (interface{}, error) {
	return ctx.v8ctx.RunScript(script, "")
}

func (ctx *ScriptContext) Window() Window {
	return ctx.window
}

// TODO: Refactor, deps are totally the wrong way around
func (ctx *ScriptContext) NewBrowserFromHandler(handler http.Handler) Browser {
	browser := NewBrowserFromHandler(handler)
	browser.ScriptEngine = ctx
	return browser
}
