package gocore

import (
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"log"
	"io"
	"os"
	"syscall"
	"os/signal"

	"strings"
	"time"
	"crypto/md5"
)

type GomixerMiddleware func(handler HandlerFunc) HandlerFunc
type Gomiddlestock map[string][]GomixerMiddleware

const (
	WARNINGCATCHSIGNAL     = "[warning] пойман один из сигналов по прекращению работы приложения...делаю дамп\n"
	defaultCookieSalt      = "e345FG345DFG$%@#$%dsfg"
)

// 404 Error handler
//type NotFoundHandler func(carry Carry) error

// The params argument contains the parameters parsed from wildcards and catch-alls in the URL.
type HandlerFunc func(carry Carry) error

//type HandlerFunc func(http.ResponseWriter, *http.Request, map[string]string, node)

//type HandlerFunc func(http.ResponseWriter, *http.Request, map[string]string)
type PanicHandler func(http.ResponseWriter, *http.Request, interface{})

// RedirectBehavior sets the behavior when the router redirects the request to the
// canonical version of the requested URL using RedirectTrailingSlash or RedirectClean.
// The default behavior is to return a 301 status, redirecting the browser to the version
// of the URL that matches the given pattern.
//
// On a POST request, most browsers that receive a 301 will submit a GET request to
// the redirected URL, meaning that any data will likely be lost. If you want to handle
// and avoid this behavior, you may use Redirect307, which causes most browsers to
// resubmit the request using the original method and request body.
//
// Since 307 is supposed to be a temporary redirect, the new 308 status code has been
// proposed, which is treated the same, except it indicates correctly that the redirection
// is permanent. The big caveat here is that the RFC is relatively recent, and older
// browsers will not know what to do with it. Therefore its use is not recommended
// unless you really know what you're doing.
//
// Finally, the UseHandler value will simply call the handler function for the pattern.
type RedirectBehavior int

type PathSource int

type showmapnode struct {
	Method   string
	Prefix   string
	Handler  *HandlerFunc
	Realpath string
}

const (
	Redirect301 RedirectBehavior = iota // Return 301 Moved Permanently
	Redirect307                         // Return 307 HTTP/1.1 Temporary Redirect
	Redirect308                         // Return a 308 RFC7538 Permanent Redirect
	UseHandler                          // Just call the handler function

	RequestURI PathSource = iota // Use r.RequestURI
	URLPath                      // Use r.URL.Path
)

type FuncTimeTicker func() error

type Gomixer struct {
	//таймеры и функционал стокеры
	timer          *time.Ticker
	timerTime      time.Duration
	stockFuncTimer []FuncTimeTicker
	stockFuncExit  []FuncTimeTicker

	//карта роутингов,создавая при добавлении любого обработчика 'prefix_subdomain':`method` + 'realpath'
	handlermap []showmapnode

	//отлов сигналов SIG*
	sigchan chan os.Signal

	//логгер
	Log     *log.Logger
	logfile string

	//миддлы
	middlewares    map[string][]GomixerMiddleware
	middlewaresAll []GomixerMiddleware

	//пул для несущей
	pool sync.Pool

	//корневая нода
	root *node

	//функционал групп (субдоменов)
	Group

	//статичный хэндлер
	StaticHandler HandlerFunc

	// The default PanicHandler just returns a 500 code.
	PanicHandler PanicHandler

	//404
	NotFoundHandler HandlerFunc

	//флаг, показываюший что обработчик выставлен сторонний, а не дефолтный
	notfoundhdnandler bool

	// Any OPTIONS request that matches a path without its own OPTIONS handler will use this handler,
	// if set, instead of calling MethodNotAllowedHandler.
	OptionsHandler HandlerFunc

	//405
	MethodNotAllowedHandler HandlerFunc

	// HeadCanUseGet allows the router to use the GET handler to respond to
	// HEAD requests if no explicit HEAD handler has been added for the
	// matching pattern. This is true by default.
	HeadCanUseGet bool

	// RedirectCleanPath allows the router to try clean the current request path,
	// if no handler is registered for it, using CleanPath from github.com/dimfeld/httppath.
	// This is true by default.
	RedirectCleanPath bool

	// RedirectTrailingSlash enables automatic redirection in case router doesn't find a matching route
	// for the current request path but a handler for the path with or without the trailing
	// slash exists. This is true by default.
	RedirectTrailingSlash bool

	// RemoveCatchAllTrailingSlash removes the trailing slash when a catch-all pattern
	// is matched, if set to true. By default, catch-all paths are never redirected.
	RemoveCatchAllTrailingSlash bool

	// RedirectBehavior sets the default redirect behavior when RedirectTrailingSlash or
	// RedirectCleanPath are true. The default value is Redirect301.
	RedirectBehavior RedirectBehavior

	// RedirectMethodBehavior overrides the default behavior for a particular HTTP method.
	// The key is the method name, and the value is the behavior to use for that method.
	RedirectMethodBehavior map[string]RedirectBehavior

	// PathSource determines from where the router gets its path to search.
	// By default it pulls the data from the RequestURI member, but this can
	// be overridden to use URL.Path instead.
	//
	// There is a small tradeoff here. Using RequestURI allows the router to handle
	// encoded slashes (i.e. %2f) in the URL properly, while URL.Path provides
	// better compatibility with some utility functions in the http
	// library that modify the Request before passing it to the router.
	PathSource PathSource
}

func (m *Gomixer) MiddlewareAddAllHandlers(middleware GomixerMiddleware) {
	m.middlewaresAll = append(m.middlewaresAll, middleware)
}
func (m *Gomixer) wrapperALLMiddlewares(handler HandlerFunc) HandlerFunc {
	//обертываем с конца слайса
	for x := len(m.middlewaresAll) - 1; x >= 0; x-- {
		//m.Log.Printf("[wrapperALLMiddlewares] %v\n", m.middlewaresAll[x])
		handler = m.middlewaresAll[x](handler)
	}
	return handler
}

func (m *Gomixer) MiddlewareAdd(prefix string, middleware GomixerMiddleware) {
	if prefix == "" {
		m.middlewares[""] = append(m.middlewares[""], middleware)
	} else {
		m.middlewares[prefix] = append(m.middlewares[prefix], middleware)
	}
}
func (m *Gomixer) wrapperMiddlewares(prefix string, handler HandlerFunc) HandlerFunc {
	//враппер миддлами для обработчиков, что берутся из пула
	//обертываем с конца стека
	stock := m.middlewares[prefix]

	for x := len(stock) - 1; x >= 0; x-- {
		//m.Log.Printf("[wrapperMiddlewares] [%s] %v\n", prefix, handler)
		handler = stock[x](handler)
	}
	return handler
}

//создаю новую несущую из пула
func (m *Gomixer) poolpop(w http.ResponseWriter, r *http.Request, n *node, params map[string]string) *Carry {
	newcarry := m.pool.Get().(*Carry)
	newcarry.w = w
	newcarry.node = n
	newcarry.params = make(map[string]string)
	if params != nil {
		newcarry.params = params
	}
	//передача контекста
	newcarry.r = r.WithContext(r.Context())
	return newcarry
}

//возврат несущей в пул
func (m *Gomixer) poolpush(r *Carry) {
	m.pool.Put(r)
}

//конвертация несущей в стандартную http.handlerfunc
func (m *Gomixer) convertCarrytoHandler(handler HandlerFunc, n *node, params map[string]string) http.HandlerFunc {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		newmux := m.poolpop(w, r, n, nil)
		handler(*newmux)
		m.poolpush(newmux)
	})
}
func (m *Gomixer) ConvertHandlerFuncToCarry(handler http.HandlerFunc) HandlerFunc {
	return m.convertHandlerToCarry(handler)
}

func (m *Gomixer) convertHandlerToCarry(handler http.HandlerFunc) HandlerFunc {
	return HandlerFunc(func(c Carry) error {
		handler(c.w, c.r)
		return nil
	})
}

// Dump returns a text representation of the routing tree.
func (m *Gomixer) Dump() string {
	return m.root.dumpTree("", "")
}

func (m *Gomixer) serveHTTPPanic(w http.ResponseWriter, r *http.Request) {
	if err := recover(); err != nil {
		m.PanicHandler(w, r, err)
	}
}

func (m *Gomixer) redirectStatusCode(method string) (int, bool) {
	var behavior RedirectBehavior
	var ok bool
	if behavior, ok = m.RedirectMethodBehavior[method]; !ok {
		behavior = m.RedirectBehavior
	}
	switch behavior {
	case Redirect301:
		return http.StatusMovedPermanently, true
	case Redirect307:
		return http.StatusTemporaryRedirect, true
	case Redirect308:
		// Go doesn'm have a constant for this yet. Yet another sign
		// that you probably shouldn'm use it.
		return 308, true
	case UseHandler:
		return 0, false
	default:
		return http.StatusMovedPermanently, true
	}
}

func redirect(w http.ResponseWriter, r *http.Request, newPath string, statusCode int) {
	newURL := url.URL{
		Path:     newPath,
		RawQuery: r.URL.RawQuery,
		Fragment: r.URL.Fragment,
	}
	http.Redirect(w, r, newURL.String(), statusCode)
}

//REDIRECT
func (m *Gomixer) RedirectCode(newPath string, c Carry, statusCode int) {
	newURL := url.URL{
		Path:     newPath,
		RawQuery: c.r.URL.RawQuery,
		Fragment: c.r.URL.Fragment,
	}
	http.Redirect(c.w, c.r, newURL.String(), statusCode)
}
func (m *Gomixer) Redirect(newPath string, c Carry) {
	newURL := url.URL{
		Path:     newPath,
		RawQuery: c.r.URL.RawQuery,
		Fragment: c.r.URL.Fragment,
	}

	http.Redirect(c.w, c.r, newURL.String(), http.StatusMovedPermanently)
}
func (m *Gomixer) MakeNewHandlerToRedirect(newpath string, statusCode int, c Carry) HandlerFunc {
	return HandlerFunc(func(c Carry) error {
		newcarry := m.poolpop(c.w, c.r, c.node, c.params)
		m.poolpush(&c)
		newURL := url.URL{
			Path:     newpath,
			RawQuery: newcarry.r.URL.RawQuery,
			Fragment: newcarry.r.URL.Fragment,
		}
		newcarry.r.URL.Path = newURL.String()
		newcarry.r.Response.StatusCode = statusCode
		return nil
	})
}

//установка собственного обработчика  404
func (m *Gomixer) Set404Handler(h HandlerFunc) {
	m.notfoundhdnandler = true
	m.NotFoundHandler = m.wrapperto404handler(h)
}
func (m *Gomixer) wrapperto404handler(h HandlerFunc) HandlerFunc {
	return HandlerFunc(func(c Carry) error {
		//выставляю флаг NotFound
		c.notfound = true
		c.staticerror = true
		//c.DataSet("func", "staticerror", true)
		//c.DataSet("func", "notfound", true)
		//m.Log.Printf("WRAPPER 404 WORKING\n")
		//возвращаю обработчик
		return h(c)
	})
}

//обработка хэндлеров
func (m *Gomixer) ServeHTTP(w http.ResponseWriter, r *http.Request) {

	if m.PanicHandler != nil {
		defer m.serveHTTPPanic(w, r)
	}

	//разбор и проверка пути
	path := r.RequestURI
	pathLen := len(path)
	if pathLen > 0 && m.PathSource == RequestURI {
		rawQueryLen := len(r.URL.RawQuery)

		if rawQueryLen != 0 || path[pathLen-1] == '?' {
			// Remove any query string and the ?.
			path = path[:pathLen-rawQueryLen-1]
			pathLen = len(path)
		}
	} else {
		// In testing with http.NewRequest,
		// RequestURI is not set so just grab URL.Path instead.
		path = r.URL.Path
		pathLen = len(path)
	}

	//разбор оконечных слешей
	trailingSlash := path[pathLen-1] == '/' && pathLen > 1
	if trailingSlash && m.RedirectTrailingSlash {
		path = path[:pathLen-1]
	}
	//поиск пути = ноды под него которая содержит обработчик по пути
	n, handler, params := m.root.search(r.Method, path[1:])
	if n == nil {
		if m.RedirectCleanPath {
			// Path was not found. Try cleaning it up and search again.
			// TODO Test this
			//cleanPath := httppath.Clean(path)
			cleanPath := Clean(path)
			n, handler, params = m.root.search(r.Method, cleanPath[1:])
			if n == nil {
				m.wrapperNotFound(w, r, nil, nil)
				return
			} else {
				if statusCode, ok := m.redirectStatusCode(r.Method); ok {
					// редирект на актуальный путь
					redirect(w, r, cleanPath, statusCode)
					return
				}
			}
		} else {
			//обработка ошибки `путь на найден`
			m.wrapperNotFound(w, r, n, nil)
			return
		}
	}

	if handler == nil {
		if r.Method == "OPTIONS" && m.OptionsHandler != nil {
			m.Log.Printf(">OptionsHandler<\n")
			handler = m.OptionsHandler
		}

		if handler == nil {
			m.wrapperMethodNotAllowed(w, r, n, nil)
			//m.MethodNotAllowedHandler(w, r, n.leafHandler)
			return
		}
	}

	if !n.isCatchAll || m.RemoveCatchAllTrailingSlash {
		if trailingSlash != n.addSlash && m.RedirectTrailingSlash {
			if statusCode, ok := m.redirectStatusCode(r.Method); ok {
				if n.addSlash {
					// Need to add a slash.
					redirect(w, r, path+"/", statusCode)
				} else if path != "/" {
					// We need to remove the slash. This was already done at the
					// beginning of the function.
					redirect(w, r, path, statusCode)
				}
				return
			}
		}
	}

	//разбор параметров
	var paramMap map[string]string
	if len(params) != 0 {
		if len(params) != len(n.leafWildcardNames) {
			// Need better behavior here. Should this be a panic?
			panic(fmt.Sprintf("httptreemux parameter list length mismatch: %v, %v",
				params, n.leafWildcardNames))
		}

		paramMap = make(map[string]string)
		numParams := len(params)
		for index := 0; index < numParams; index++ {
			paramMap[n.leafWildcardNames[numParams-index-1]] = params[index]
		}
	}
	//получаю carry из пула
	carry := m.poolpop(w, r, n, paramMap)

	//обертываю миддлами
	hu := m.wrapperMiddlewares(n.prefix, handler)

	//обертываю миддлами для всех обработчиков
	hu = m.wrapperALLMiddlewares(hu)

	//проверка на статичный путь
	hu(*carry)
	//возврат в пул
	m.poolpush(carry)
}

//обертка для обработки значения не найдено
func (m *Gomixer) wrapperNotFound(w http.ResponseWriter, r *http.Request, n *node, param map[string]string) {
	carry := m.poolpop(w, r, n, nil)
	shu := m.wrapperALLMiddlewares(m.NotFoundHandler)
	if n == nil {
		shu = m.wrapperMiddlewares("", shu)
	}
	//carry.DataSet("func", "notfound", true)
	shu(*carry)
	m.poolpush(carry)
	return
}
func (m *Gomixer) wrapperMethodNotAllowed(w http.ResponseWriter, r *http.Request, n *node, param map[string]string) {
	carry := m.poolpop(w, r, n, nil)
	shu := m.wrapperALLMiddlewares(m.MethodNotAllowedHandler)
	if n == nil {
		shu = m.wrapperMiddlewares("", shu)
	}
	//carry.DataSet("func", "methodnotfound", true)
	shu(*carry)
	m.poolpush(carry)
	return
}

//обертка для обработки статичного контекста
func (m *Gomixer) wrapperStatic(w http.ResponseWriter, r *http.Request, n *node, param map[string]string) {
	carry := m.poolpop(w, r, n, nil)
	shu := m.wrapperALLMiddlewares(m.staticHandler)
	shu = m.wrapperMiddlewares(n.prefix, shu)
	shu(*carry)
	m.poolpush(carry)
	return
}

func (m *Gomixer) staticHandler(c Carry) error {
	if strings.HasSuffix(c.r.URL.Path, "/") {
		//c.DataSet("func", "staticerror", true)
		m.NotFoundHandler(c)
		return nil
	}
	//проверка на наличия файла по статичному пути вообще
	fs := justFilesFilesystem{http.Dir(c.node.staticreal), m, c}
	_, err := fs.Open(c.r.URL.Path)
	if err != nil {
		//c.DataSet("func", "staticerror", true)
		m.NotFoundHandler(c)
		return nil
	}
	hand := http.StripPrefix(c.node.staticprefix, http.FileServer(fs))
	hand.ServeHTTP(c.ResponseWriter(), c.Request())

	return nil
}

//---------------------------------------------------------------------------
//  КУКИСЫ
//---------------------------------------------------------------------------
func (m *Gomixer) NewCook(cookName string, salt string, c Carry) (http.Cookie) {
	cook := http.Cookie{}
	cook.Name = cookName
	if len(salt) > 0 {
		cook.Value = m.cookgeneratenew(salt)
	} else {
		cook.Value = m.cookgeneratenew(defaultCookieSalt)
	}
	cook.Expires = time.Now().Add(time.Duration(86000*30) * time.Minute)
	cook.Path = "/"
	return cook
}

//генерация нового значения для кукиса
func (m *Gomixer) cookgeneratenew(salt string) string {
	return fmt.Sprintf("%x", md5.Sum([]byte(time.Now().String()+salt)))
}

func (m *Gomixer) TimerFuncAdd(f FuncTimeTicker) {
	m.stockFuncTimer = append(m.stockFuncTimer, f)
}

//---------------------------------------------------------------------------
//  проверка на наличия файла
//---------------------------------------------------------------------------
type justFilesFilesystem struct {
	fs  http.FileSystem
	mux *Gomixer
	c   Carry
}

func (fs justFilesFilesystem) Open(name string) (http.File, error) {
	f, err := fs.fs.Open(name)
	if err != nil {
		fs.mux.Log.Printf("ERROR ACCESS FILE DIR or FILE `%v`\n", err)
		return nil, err
	}
	return neuteredReaddirFile{f}, nil
}

type neuteredReaddirFile struct {
	http.File
}

func (f neuteredReaddirFile) Readdir(count int) ([]os.FileInfo, error) {
	return nil, nil
}

//дефолтный обработчик 404 ошибки
func NotFoundHandler(c Carry) error {
	c.notfound = true
	c.staticerror = true
	return c.WriteHTML(http.StatusNotFound, "<h3><b>404</b> NOT FOUND RESOURCE</h3>")
}

//дефолтный обработчик 405 ошибки
func MethodNotAllowedHandler(c Carry) error {
	return c.WriteHTML(http.StatusMethodNotAllowed, "<h3><b>405</b> METHOD NOT ALLOWED</h3>")
}

//отобразить карту обработчиков
func (g *Gomixer) ShowHandlersMap() {
	for _, v := range g.handlermap {
		g.Log.Printf("PREFIX: `%-30s`METHOD: `%-30s`REALPATH:`%-100s`\n",
			v.Prefix, v.Method, v.Realpath)
	}
}

func New(logout io.Writer, timerTime time.Duration) *Gomixer {
	tm := &Gomixer{
		root:                    &node{path: "/"},
		NotFoundHandler:         NotFoundHandler,
		MethodNotAllowedHandler: MethodNotAllowedHandler,
		HeadCanUseGet:           true,
		RedirectTrailingSlash:   true,
		RedirectCleanPath:       true,
		RedirectBehavior:        Redirect301,
		RedirectMethodBehavior:  make(map[string]RedirectBehavior),
		PathSource:              RequestURI,
		pool:                    sync.Pool{},
		middlewares:             make(map[string][]GomixerMiddleware),
		Log:                     log.New(logout, PREFIXLOGGER, log.Ltime|log.Ldate|log.Lshortfile),
		sigchan:                 make(chan os.Signal, 2),
		timerTime:               timerTime,
		timer:                   time.NewTicker(timerTime),
	}

	//запуск горутины проверяющий и запускающий функционал из тикерстека
	go tm.timerFuncRuner()

	//сигналы оповещателю сигналов от системы
	signal.Notify(tm.sigchan, os.Interrupt,
		syscall.SIGHUP,
		syscall.SIGINT,
		syscall.SIGTERM,
		syscall.SIGQUIT)
	//запускаю воркер в горутине по анализу и реакции на сигналы
	go tm.workerCatchSignals()
	//дефолт функция для несущей
	tm.pool.New = func() interface{} {
		return &Carry{
			mix:   *tm,
			r:     &http.Request{},
		}
	}

	tm.Group.mux = tm

	return tm
}
func (m *Gomixer) AddStockFuncExit(fu FuncTimeTicker) {
	m.stockFuncExit = append(m.stockFuncExit, fu)
}

//горутина ловящая прерывания приложения для корректного завершения
func (m *Gomixer) workerCatchSignals() {
	defer func() {
		m.Log.Printf("[workerCatchSignals] закончил..\n")
	}()
	m.Log.Printf("[workerCatchSignals] стартанул..\n")
	sig := <-m.sigchan
	switch sig {
	case syscall.SIGXFSZ:
		fallthrough
	case syscall.SIGXCPU:
		fallthrough
	case syscall.SIGSYS:
		fallthrough
	case syscall.SIGTTOU:
		fallthrough
	case syscall.SIGTTIN:
		fallthrough
	case syscall.SIGSTOP:
		fallthrough
	case syscall.SIGSEGV:
		fallthrough
	case syscall.SIGPIPE:
		fallthrough
	case syscall.SIGHUP:
		fallthrough
	case os.Interrupt:
		fallthrough
	case syscall.SIGTERM:
		fallthrough
	case syscall.SIGQUIT:
		fallthrough
	case syscall.SIGKILL:
		m.Log.Printf(WARNINGCATCHSIGNAL)
		if len(m.stockFuncExit) > 0 {
			//обработка происходит линейно во избежания прерывания
			//выполнения функционала при выходе из
			//основного потока
			for _, fu := range m.stockFuncExit {
				err := fu()
				if err != nil {
					m.Log.Printf(err.Error())
				}
			}
		}
		//выхожу из приложения
		os.Exit(-1)
		return

	}
}

func (m *Gomixer) timerFuncRuner() {
	defer func() {
		m.Log.Printf("[timerFuncRuner] закончил..\n")
	}()
	m.Log.Printf("[timerFuncRuner] стартанул..\n")
	for {
		select {
		case <-m.timer.C:
			if len(m.stockFuncTimer) > 0 {
				for _, f := range m.stockFuncTimer {
					err := f()
					if err != nil {
						m.Log.Printf(err.Error())
					}
				}
			}
		default:
			time.Sleep(time.Second * 2)
		}
	}
}

//---------------------------------------------------------------------------
//  show middlewares
//---------------------------------------------------------------------------
func (m *Gomixer) ShowMiddlewares(all bool) {
	if all {
		//	show slice all middlewares
		if len(m.middlewaresAll) == 0 {
			m.Log.Printf("no global middlewares")
			return
		}
		for x, z := range m.middlewaresAll {
			m.Log.Printf("[global] [%3d] [middleware] %v\n", x, z)
		}
	} else {
		//show  map  middlewares
		if len(m.middlewares) == 0 {
			m.Log.Printf("no middlewares")
			return
		}
		if len(m.middlewares) > 0 {
			for x, v := range m.middlewares {
				if len(v) > 0 {
					for a, z := range v {
						m.Log.Printf("[prefix: %s] [ %3d ] [middleware] %v\n", x, a, z)
					}
				}

			}
		}
	}
}
