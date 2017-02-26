package homehub

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

type apiTest struct {
	method          string
	methodArgs      []interface{}
	apiStubResponse string
	expectedResult  string
	t               *testing.T
}

func getEnv(name string, defaultValue string) string {
	value := os.Getenv(name)
	if value == "" {
		return defaultValue
	}
	return value
}

func santizeString(target *string, regex string, replacement string) {
	re := regexp.MustCompile(regex)
	for _, match := range re.FindAllString(*target, -1) {
		*target = strings.Replace(*target, match, replacement, -1)
	}
}

func stubbedResponseHTTPHandler(apiStubResponse string, w http.ResponseWriter, r *http.Request) {
	var stubDataFile string
	if strings.HasSuffix(r.RequestURI, "/eventLog") {
		stubDataFile = "testdata/eventLog.txt"
	} else if strings.HasSuffix(r.RequestURI, "/stats.csv") {
		stubDataFile = "testdata/stats.csv"
	} else {
		stubDataFile = "testdata/" + apiStubResponse + "_response.json"
	}

	bytesRead, err := ioutil.ReadFile(stubDataFile)
	if err == nil {
		fmt.Fprintln(w, string(bytesRead))
	} else {
		fmt.Fprintln(w, "{\"reply\": { \"uid\": 0 \"id\": 0 \"error\": \"code\": 99999999, \"description\": \""+err.Error()+"\" }}")
	}
}

func proxiedResponseHTTPHandler(apiStubResponse string, url string, w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequest(r.Method, url+r.RequestURI, r.Body)
	req.ContentLength = r.ContentLength
	req.Form = r.Form
	req.Header = r.Header

	for _, cookie := range r.Cookies() {
		req.AddCookie(cookie)
	}

	httpClient := &http.Client{}
	httpResponse, err := httpClient.Do(req)
	if err != nil {
		fmt.Fprintln(w, "{\"reply\": { \"uid\": 0 \"id\": 0 \"error\": { \"code\": 99999999, \"description\": \""+err.Error()+"\" }}}")
		return
	}

	defer httpResponse.Body.Close()
	bodyBytes, err := ioutil.ReadAll(httpResponse.Body)
	if err != nil {
		fmt.Fprintln(w, "{\"reply\": { \"uid\": 0 \"id\": 0 \"error\": { \"code\": 99999999, \"description\": \"Error reading proxied response\" }}}")
		return
	}

	body := string(bodyBytes[:])

	// Clean up MAC addresses
	santizeString(&body, "\\b([0-9a-fA-F]{2}:??){5}([0-9a-fA-F]{2})\\b", "11:AA:2B:33:44:5C")
	// Clean up IP addresses
	santizeString(&body, "\\b((25[0-5]|2[0-4]\\d|[0-1]?\\d?\\d)(\\.(25[0-5]|2[0-4]\\d|[0-1]?\\d?\\d)){3})\\b", "192.168.1.68")
	// Clean up timestampts
	santizeString(&body, "\\b([0-9]{4})-([0-9]{2})-([0-9]{2})T([0-9]{2}):([0-9]{2}):([0-9]{2})\\+([0-9]{4})\\b", "2016-08-30T19:48:55+0100")
	// Clean up serial number
	santizeString(&body, "\\b([0-9]{6})\\+([A-Z]{2})([0-9]{8})\\b", "123456+NQ98765432")

	var dat map[string]interface{}
	err = json.Unmarshal([]byte(body), &dat)
	if err != nil {
		fmt.Fprintln(w, "{\"reply\": { \"uid\": 0 \"id\": 0 \"error\": { \"code\": 99999999, \"description\": \"Error unmarshalling JSON response\" }}}")
		return
	}

	json, err := json.MarshalIndent(dat, "", "  ")
	if err != nil {
		fmt.Fprintln(w, "{\"reply\": { \"uid\": 0 \"id\": 0 \"error\": { \"code\": 99999999, \"description\": \"Error marshalling JSON response\" }}}")
		return
	}

	ioutil.WriteFile("testdata/"+apiStubResponse+"_response.json", json, 0644)
	fmt.Fprintln(w, body)
}

func mockAPIClientServer(apiStubResponse string) (*httptest.Server, *Hub) {
	defaultUsername := "admin"
	defaultPassword := "passw0rd"
	username := getEnv("HUB_USERNAME", defaultUsername)
	password := getEnv("HUB_PASSWORD", defaultPassword)
	debug := getEnv("HUB_DEBUG", "false")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if username == defaultUsername && password == defaultPassword {
			stubbedResponseHTTPHandler(apiStubResponse, w, r)
		} else {
			proxiedResponseHTTPHandler(apiStubResponse, os.Getenv("HUB_URL"), w, r)
		}
	}))

	url := getEnv("HUB_URL", server.URL)
	hub := New(server.URL, username, password)

	if debug == "true" {
		hub.EnableDebug(true)
	}

	if url != server.URL {
		hub.Login()
	} else {
		hub.client.authData.userName = "admin"
		hub.client.authData.password = "admin"
		hub.client.authData.sessionID = "987879"
		hub.client.authData.nonce = "2355345"
	}

	return server, hub
}

func testAPIResponse(a *apiTest) {
	server, hub := mockAPIClientServer(a.apiStubResponse)
	defer server.Close()

	v := reflect.TypeOf(hub)

	apiMethod, _ := v.MethodByName(a.method)

	inputs := make([]reflect.Value, len(a.methodArgs)+1)
	for i := range a.methodArgs {
		inputs[i+1] = reflect.ValueOf(a.methodArgs[i])
	}

	inputs[0] = reflect.ValueOf(hub)
	resp := apiMethod.Func.Call(inputs)
	result := ""

	if resp[0].Type().String() == "string" {
		result = resp[0].String()
	} else if resp[0].Type().String() == "error" {
		if !resp[0].IsNil() {
			a.t.Fatalf("API method %s returned an unexpected error", a.method)
		}
	}

	if len(resp) > 1 {
		if !resp[1].IsNil() {
			if resp[1].Type().String() == "error" {
				result = fmt.Sprintf("%s", resp[1].Interface())
			}
		}
	}

	if result != a.expectedResult {
		a.t.Fatalf("API method %s returned '%s'. Expected '%s'", a.method, result, a.expectedResult)
	}
}

func TestBandwidthMonitor(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "BandwidthMonitor",
		apiStubResponse: "bandwidth_monitor",
		expectedResult:  "a0:b1:c2:d3:e4:f5,2016-12-31,10959,1301\na1:b9:c8:d7:e6:f5,2016-12-31,218,30\n\n",
		t:               t,
	})
}

func TestBroadbandProductType(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "BroadbandProductType",
		apiStubResponse: "interface_type",
		expectedResult:  "BT Infinity",
		t:               t,
	})
}

func TestConnectedDevices(t *testing.T) {
	var buffer bytes.Buffer

	buffer.WriteString("\n")
	buffer.WriteString("--   ----------          ----------------         ----   \n")
	buffer.WriteString("ID   IP Address          Physical Address         Type   \n")
	buffer.WriteString("--   ----------          ----------------         ----   \n")
	buffer.WriteString("2    192.168.1.68        11:AA:2B:33:44:5C        Ethernet\n")
	buffer.WriteString("3    192.168.1.68        11:AA:2B:33:44:5C        Ethernet\n")

	testAPIResponse(&apiTest{
		method:          "ConnectedDevices",
		apiStubResponse: "connected_devices",
		expectedResult:  buffer.String(),
		t:               t,
	})
}

func TestDataPumpVersion(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "DataPumpVersion",
		apiStubResponse: "data_pump_version",
		expectedResult:  "AfH042f.d26k1\n",
		t:               t,
	})
}

func TestDataReceived(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "DataReceived",
		apiStubResponse: "data_received",
		expectedResult:  "99887766",
		t:               t,
	})
}

func TestDataSent(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "DataSent",
		apiStubResponse: "data_sent",
		expectedResult:  "11223344",
		t:               t,
	})
}

func TestDeviceInfo(t *testing.T) {

	var buffer bytes.Buffer
	buffer.WriteString("\n")
	buffer.WriteString("--   ----------          ----------------         ----   \n")
	buffer.WriteString("ID   IP Address          Physical Address         Type   \n")
	buffer.WriteString("--   ----------          ----------------         ----   \n")
	buffer.WriteString("2    192.168.1.68        11:AA:2B:33:44:5C        Ethernet\n")

	testAPIResponse(&apiTest{
		method:          "DeviceInfo",
		methodArgs:      []interface{}{2},
		apiStubResponse: "device_info",
		expectedResult:  buffer.String(),
		t:               t,
	})
}

func TestDhcpAuthoritative(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "DhcpAuthoritative",
		apiStubResponse: "dhcp_authoritative",
		expectedResult:  "true",
		t:               t,
	})
}

func TestDhcpPoolStart(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "DhcpPoolStart",
		apiStubResponse: "dhcp_ipv4_pool_start",
		expectedResult:  "192.168.1.68",
		t:               t})
}

func TestDhcpPoolEnd(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "DhcpPoolEnd",
		apiStubResponse: "dhcp_ipv4_pool_end",
		expectedResult:  "192.168.1.253",
		t:               t,
	})
}

func TestDhcpSubnetMask(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "DhcpSubnetMask",
		apiStubResponse: "dhcp_subnet_mask",
		expectedResult:  "255.255.255.0",
		t:               t,
	})
}

func TestDownstreamSyncSpeed(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "DownstreamSyncSpeed",
		apiStubResponse: "downstream_curr_rate",
		expectedResult:  "97543",
		t:               t,
	})
}

func TestEventLog(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "EventLog",
		apiStubResponse: "event_log",
		expectedResult:  "event 1\nevent 2\n\n",
		t:               t,
	})
}

func TestHardwareVersion(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "HardwareVersion",
		apiStubResponse: "hardware_version",
		expectedResult:  "1.0",
		t:               t,
	})
}

func TestInternetConnectionStatus(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "InternetConnectionStatus",
		apiStubResponse: "wan_internet_status",
		expectedResult:  "UP",
		t:               t,
	})
}

func TestIsLoggedInFalse(t *testing.T) {
	hub := New("", "", "")

	if hub.IsLoggedIn() {
		t.Errorf("Expected IsLoggedIn to return false")
	}
}

func TestIsLoggedInTrue(t *testing.T) {
	server, hub := mockAPIClientServer("login")
	defer server.Close()

	hub.Login()
	if !hub.IsLoggedIn() {
		t.Errorf("Expected IsLoggedIn to return true")
	}
}

func TestLightBrightness(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "LightBrightness",
		apiStubResponse: "hub_light_brightness",
		expectedResult:  "50",
		t:               t,
	})
}

func TestLightBrightnessSet(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "LightBrightnessSet",
		methodArgs:      []interface{}{50},
		apiStubResponse: "hub_light_brightness_set",
		t:               t,
	})
}

func TestLightEnable(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "LightEnable",
		methodArgs:      []interface{}{true},
		apiStubResponse: "hub_light_enable",
		t:               t,
	})
}

func TestLightStatus(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "LightStatus",
		apiStubResponse: "hub_light_status",
		expectedResult:  "OFF",
		t:               t,
	})
}

func TestLoginSuccess(t *testing.T) {
	server, hub := mockAPIClientServer("login")
	defer server.Close()

	loggedIn, err := hub.Login()

	if err != nil {
		t.Error(err)
	}

	if !loggedIn {
		t.Errorf("Expected login to be successful")
	}
}

func TestLocalTime(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "LocalTime",
		apiStubResponse: "ntp_local_time",
		expectedResult:  "2016-08-30T19:48:55+0100",
		t:               t,
	})
}

func TestMaintenanceFirmwareVersion(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "MaintenaceFirmwareVersion",
		apiStubResponse: "maintenance_firmware_version",
		expectedResult:  "SG0B000000AA",
		t:               t,
	})
}

func TestPublicIPAddress(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "PublicIPAddress",
		apiStubResponse: "public_ip4",
		expectedResult:  "192.168.1.68",
		t:               t,
	})
}

func TestPublicSubnetMask(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "PublicSubnetMask",
		apiStubResponse: "public_subnet_mask",
		expectedResult:  "255.255.255.255",
		t:               t,
	})
}

func TestReboot(t *testing.T) {
	// If we're testing against the real router, we don't want to reboot it midway throuh the test suite!
	if os.Getenv("HUB_USERNAME") != "" && os.Getenv("HUB_PASSWORD") != "" {
		testAPIResponse(&apiTest{
			method:          "Reboot",
			apiStubResponse: "reboot",
			expectedResult:  "",
			t:               t,
		})
	}
}

func TestSambaHost(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "SambaHost",
		apiStubResponse: "samba_host",
		expectedResult:  "bthub,hub,bthomehub,api",
		t:               t,
	})
}

func TestSambaIP(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "SambaIP",
		apiStubResponse: "samba_ip",
		expectedResult:  "192.168.1.68",
		t:               t,
	})
}

func TestSerialNumber(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "SerialNumber",
		apiStubResponse: "serial_number",
		expectedResult:  "+123456+NQ98765432",
		t:               t,
	})
}

func TestSessionExpired(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "SerialNumber",
		apiStubResponse: "session_expired",
		expectedResult:  "Invalid user session",
		t:               t,
	})
}

func TestSoftwareVersion(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "SoftwareVersion",
		apiStubResponse: "software_version",
		expectedResult:  "SG4B10002244",
		t:               t,
	})
}

func TestUpstreamSyncSpeed(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "UpstreamSyncSpeed",
		apiStubResponse: "upstream_curr_rate",
		expectedResult:  "52121",
		t:               t,
	})
}

func TestVersion(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "Version",
		apiStubResponse: "hub_version",
		expectedResult:  "Home Hub 60 Type A",
		t:               t,
	})
}

func TestWiFiSecurityMode(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "WiFiSecurityMode",
		apiStubResponse: "wifi24_security_mode",
		expectedResult:  "ULTRA_SECURE_MODE",
		t:               t,
	})
}

func TestWiFiSSID(t *testing.T) {
	testAPIResponse(&apiTest{
		method:          "WiFiSSID",
		apiStubResponse: "wifi24_ssid",
		expectedResult:  "Click here for viruses",
		t:               t,
	})
}
