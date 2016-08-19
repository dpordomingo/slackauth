package slackauth

import (
	"errors"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/nlopes/slack"

	log15 "gopkg.in/inconshreveable/log15.v2"
)

// Service is a service to authenticate on slack using the "Add to slack" button.
type Service interface {
	// SetLogOutput sets the place where logs will be written.
	SetLogOutput(io.Writer)

	// Run will run the service. This method blocks until the service crashes or stops.
	Run() error

	// OnAuth sets the handler that will be triggered every time someone authorizes slack
	// successfully.
	OnAuth(func(*slack.OAuthResponse))
}

type slackAPI interface {
	GetOAuthResponse(string, string, string, bool) (*slack.OAuthResponse, error)
}

type slackAPIWrapper struct{}

func (*slackAPIWrapper) GetOAuthResponse(id, secret, code string, debug bool) (*slack.OAuthResponse, error) {
	if debug {
		slack.SetLogger(log.New(os.Stdout, "", log.LstdFlags))
	}
	return slack.GetOAuthResponse(id, secret, code, "", debug)
}

type slackAuth struct {
	clientID     string
	clientSecret string
	addr         string
	certFile     string
	keyFile      string
	successTpl   *template.Template
	errorTpl     *template.Template
	debug        bool
	auths        chan *slack.OAuthResponse
	callback     func(*slack.OAuthResponse)
	api          slackAPI
}

// Options has all the configurable parameters for slack authenticator.
type Options struct {
	Addr         string
	ClientID     string
	ClientSecret string
	SuccessTpl   string
	ErrorTpl     string
	Debug        bool
	CertFile     string
	KeyFile      string
}

// New creates a new slackauth service.
func New(opts Options) (Service, error) {
	if opts.Addr == "" || opts.ClientID == "" || opts.ClientSecret == "" {
		return nil, errors.New("slackauth: addr, client id and client secret can not be empty")
	}

	successTpl, err := readTemplate(opts.SuccessTpl)
	if err != nil {
		return nil, err
	}

	errorTpl, err := readTemplate(opts.ErrorTpl)
	if err != nil {
		return nil, err
	}

	return &slackAuth{
		clientID:     opts.ClientID,
		clientSecret: opts.ClientSecret,
		addr:         opts.Addr,
		successTpl:   successTpl,
		errorTpl:     errorTpl,
		debug:        opts.Debug,
		certFile:     opts.CertFile,
		keyFile:      opts.KeyFile,
		auths:        make(chan *slack.OAuthResponse, 1),
		api:          &slackAPIWrapper{},
	}, nil
}

func (s *slackAuth) Run() error {
	go func() {
		for auth := range s.auths {
			if s.callback != nil {
				s.callback(auth)
			} else {
				log15.Warn("auth event triggered but there was no handler")
			}
		}
	}()

	return s.runServer()
}

func (s *slackAuth) SetLogOutput(w io.Writer) {
	var nilWriter io.Writer

	var format = log15.LogfmtFormat()
	if w == nilWriter || w == nil {
		w = os.Stdout
		format = log15.TerminalFormat()
	}

	var maxLvl = log15.LvlInfo
	if s.debug {
		maxLvl = log15.LvlDebug
	}

	log15.Root().SetHandler(log15.LvlFilterHandler(maxLvl, log15.StreamHandler(w, format)))
}

func (s *slackAuth) OnAuth(fn func(*slack.OAuthResponse)) {
	s.callback = fn
}

func (s *slackAuth) runServer() error {
	srv := &http.Server{
		ReadTimeout:  1 * time.Second,
		WriteTimeout: 3 * time.Second,
		Addr:         s.addr,
		Handler:      s,
	}

	if s.certFile != "" && s.keyFile != "" {
		return srv.ListenAndServeTLS(s.certFile, s.keyFile)
	}
	return srv.ListenAndServe()
}

func (s *slackAuth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	resp, err := s.api.GetOAuthResponse(s.clientID, s.clientSecret, code, s.debug)
	if err != nil {
		log15.Error("error getting oauth response", "err", err.Error())
		if err := s.errorTpl.Execute(w, resp); err != nil {
			log15.Error("error displaying error tpl", "err", err.Error())
		}
		return
	}

	if err := s.successTpl.Execute(w, resp); err != nil {
		log15.Error("error displaying success tpl", "err", err.Error())
	}

	s.auths <- resp
}

func readTemplate(file string) (*template.Template, error) {
	bytes, err := ioutil.ReadFile(file)
	if err != nil {
		return nil, err
	}

	return template.New("").Parse(string(bytes))
}