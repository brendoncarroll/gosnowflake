// Copyright (c) 2017-2018 Snowflake Computing Inc. All right reserved.

package gosnowflake

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	clientType = "Go"
)

const (
	authenticatorExternalBrowser = "EXTERNALBROWSER"
	authenticatorOAuth           = "OAUTH"
	authenticatorSnowflake       = "SNOWFLAKE"
	authenticatorOkta            = "OKTA"
)

// platform consists of compiler and architecture type in string
var platform = fmt.Sprintf("%v-%v", runtime.Compiler, runtime.GOARCH)

// operatingSystem is the runtime operating system.
var operatingSystem = runtime.GOOS

// userAgent shows up in User-Agent HTTP header
var userAgent = fmt.Sprintf("%v/%v/%v/%v", clientType, SnowflakeGoDriverVersion, runtime.Version(), platform)

type authRequestClientEnvironment struct {
	Application string `json:"APPLICATION"`
	Os          string `json:"OS"`
	OsVersion   string `json:"OS_VERSION"`
}
type authRequestData struct {
	ClientAppID             string                       `json:"CLIENT_APP_ID"`
	ClientAppVersion        string                       `json:"CLIENT_APP_VERSION"`
	SvnRevision             string                       `json:"SVN_REVISION"`
	AccountName             string                       `json:"ACCOUNT_NAME"`
	LoginName               string                       `json:"LOGIN_NAME,omitempty"`
	Password                string                       `json:"PASSWORD,omitempty"`
	RawSAMLResponse         string                       `json:"RAW_SAML_RESPONSE,omitempty"`
	ExtAuthnDuoMethod       string                       `json:"EXT_AUTHN_DUO_METHOD,omitempty"`
	Passcode                string                       `json:"PASSCODE,omitempty"`
	Authenticator           string                       `json:"AUTHENTICATOR,omitempty"`
	SessionParameters       map[string]string            `json:"SESSION_PARAMETERS,omitempty"`
	ClientEnvironment       authRequestClientEnvironment `json:"CLIENT_ENVIRONMENT"`
	BrowserModeRedirectPort string                       `json:"BROWSER_MODE_REDIRECT_PORT,omitempty"`
	ProofKey                string                       `json:"PROOF_KEY,omitempty"`
	Token                   string                       `json:"TOKEN,omitempty"`
}
type authRequest struct {
	Data authRequestData `json:"data"`
}

type nameValueParameter struct {
	Name  string      `json:"name"`
	Value interface{} `json:"value"`
}

type authResponseSessionInfo struct {
	DatabaseName  string `json:"databaseName"`
	SchemaName    string `json:"schemaName"`
	WarehouseName string `json:"warehouseName"`
	RoleName      string `json:"roleName"`
}

type authResponseMain struct {
	Token                   string                  `json:"token,omitempty"`
	ValidityInSeconds       time.Duration           `json:"validityInSeconds,omitempty"`
	MasterToken             string                  `json:"masterToken,omitempty"`
	MasterValidityInSeconds time.Duration           `json:"masterValidityInSeconds"`
	DisplayUserName         string                  `json:"displayUserName"`
	ServerVersion           string                  `json:"serverVersion"`
	FirstLogin              bool                    `json:"firstLogin"`
	RemMeToken              string                  `json:"remMeToken"`
	RemMeValidityInSeconds  time.Duration           `json:"remMeValidityInSeconds"`
	HealthCheckInterval     time.Duration           `json:"healthCheckInterval"`
	NewClientForUpgrade     string                  `json:"newClientForUpgrade"`
	SessionID               int                     `json:"sessionId"`
	Parameters              []nameValueParameter    `json:"parameters"`
	SessionInfo             authResponseSessionInfo `json:"sessionInfo"`
	TokenURL                string                  `json:"tokenUrl,omitempty"`
	SSOURL                  string                  `json:"ssoUrl,omitempty"`
	ProofKey                string                  `json:"proofKey,omitempty"`
}
type authResponse struct {
	Data    authResponseMain `json:"data"`
	Message string           `json:"message"`
	Code    string           `json:"code"`
	Success bool             `json:"success"`
}

func postAuth(
	sr *snowflakeRestful,
	params *url.Values,
	headers map[string]string,
	body []byte,
	timeout time.Duration) (
	data *authResponse, err error) {
	params.Add("requestId", uuid.New().String())
	fullURL := fmt.Sprintf(
		"%s://%s:%d%s", sr.Protocol, sr.Host, sr.Port,
		"/session/v1/login-request?"+params.Encode())
	glog.V(2).Infof("full URL: %v", fullURL)
	resp, err := sr.FuncPost(context.TODO(), sr, fullURL, headers, body, timeout, true)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK {
		var respd authResponse
		err = json.NewDecoder(resp.Body).Decode(&respd)
		if err != nil {
			glog.V(1).Infof("failed to decode JSON. err: %v", err)
			glog.Flush()
			return nil, err
		}
		return &respd, nil
	}
	switch resp.StatusCode {
	case http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		// service availability or connectivity issue. Most likely server side issue.
		return nil, &SnowflakeError{
			Number:      ErrCodeServiceUnavailable,
			SQLState:    SQLStateConnectionWasNotEstablished,
			Message:     errMsgServiceUnavailable,
			MessageArgs: []interface{}{resp.StatusCode, fullURL},
		}
	case http.StatusUnauthorized, http.StatusForbidden:
		// failed to connect to db. account name may be wrong
		return nil, &SnowflakeError{
			Number:      ErrCodeFailedToConnect,
			SQLState:    SQLStateConnectionRejected,
			Message:     errMsgFailedToConnect,
			MessageArgs: []interface{}{resp.StatusCode, fullURL},
		}
	}
	b, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		glog.V(1).Infof("failed to extract HTTP response body. err: %v", err)
		glog.Flush()
		return nil, err
	}
	glog.V(1).Infof("HTTP: %v, URL: %v, Body: %v", resp.StatusCode, fullURL, b)
	glog.V(1).Infof("Header: %v", resp.Header)
	glog.Flush()
	return nil, &SnowflakeError{
		Number:      ErrFailedToAuth,
		SQLState:    SQLStateConnectionRejected,
		Message:     errMsgFailedToAuth,
		MessageArgs: []interface{}{resp.StatusCode, fullURL},
	}
}

// Generates a map of headers needed to authenticate
// with Snowflake.
func getHeaders() map[string]string {
	headers := make(map[string]string)
	headers["Content-Type"] = headerContentTypeApplicationJSON
	headers["accept"] = headerAcceptTypeApplicationSnowflake
	headers["User-Agent"] = userAgent
	return headers
}

// Used to authenticate the user with Snowflake.
func authenticate(
	sc *snowflakeConn,
	samlResponse []byte,
	proofKey []byte,
) (resp *authResponseMain, err error) {

	headers := getHeaders()
	clientEnvironment := authRequestClientEnvironment{
		Application: sc.cfg.Application,
		Os:          operatingSystem,
		OsVersion:   platform,
	}

	sessionParameters := make(map[string]string)
	for k, v := range sc.cfg.Params {
		// upper casing to normalize keys
		sessionParameters[strings.ToUpper(k)] = *v
	}

	requestMain := authRequestData{
		ClientAppID:       clientType,
		ClientAppVersion:  SnowflakeGoDriverVersion,
		AccountName:       sc.cfg.Account,
		SessionParameters: sessionParameters,
		ClientEnvironment: clientEnvironment,
	}

	authenticator := strings.ToUpper(sc.cfg.Authenticator)
	switch authenticator {
	case authenticatorExternalBrowser:
		requestMain.ProofKey = string(proofKey)
		requestMain.Token = string(samlResponse)
		requestMain.LoginName = sc.cfg.User
		requestMain.Authenticator = authenticatorExternalBrowser
	case authenticatorOAuth:
		requestMain.LoginName = sc.cfg.User
		requestMain.Authenticator = authenticatorOAuth
		requestMain.Token = sc.cfg.Token
	case authenticatorOkta:
		requestMain.RawSAMLResponse = string(samlResponse)
	case authenticatorSnowflake:
		fallthrough
	default:
		glog.V(2).Info("Username and password")
		requestMain.LoginName = sc.cfg.User
		requestMain.Password = sc.cfg.Password
		switch {
		case sc.cfg.PasscodeInPassword:
			requestMain.ExtAuthnDuoMethod = "passcode"
		case sc.cfg.Passcode != "":
			requestMain.Passcode = sc.cfg.Passcode
			requestMain.ExtAuthnDuoMethod = "passcode"
		}
	}

	authRequest := authRequest{
		Data: requestMain,
	}
	params := &url.Values{}
	if sc.cfg.Database != "" {
		params.Add("databaseName", sc.cfg.Database)
	}
	if sc.cfg.Schema != "" {
		params.Add("schemaName", sc.cfg.Schema)
	}
	if sc.cfg.Warehouse != "" {
		params.Add("warehouse", sc.cfg.Warehouse)
	}
	if sc.cfg.Role != "" {
		params.Add("roleName", sc.cfg.Role)
	}

	jsonBody, err := json.Marshal(authRequest)
	if err != nil {
		return
	}

	glog.V(2).Infof("PARAMS for Auth: %v, %v, %v, %v, %v, %v",
		params, sc.rest.Protocol, sc.rest.Host, sc.rest.Port, sc.rest.LoginTimeout, sc.rest.Authenticator)

	respd, err := sc.rest.FuncPostAuth(sc.rest, params, headers, jsonBody, sc.rest.LoginTimeout)
	if err != nil {
		return nil, err
	}
	if !respd.Success {
		glog.V(1).Infoln("Authentication FAILED")
		glog.Flush()
		sc.rest.Token = ""
		sc.rest.MasterToken = ""
		sc.rest.SessionID = -1
		code, err := strconv.Atoi(respd.Code)
		if err != nil {
			code = -1
			return nil, err
		}
		return nil, &SnowflakeError{
			Number:   code,
			SQLState: SQLStateConnectionRejected,
			Message:  respd.Message,
		}
	}
	glog.V(2).Info("Authentication SUCCESS")
	sc.rest.Token = respd.Data.Token
	sc.rest.MasterToken = respd.Data.MasterToken
	sc.rest.SessionID = respd.Data.SessionID
	return &respd.Data, nil
}
