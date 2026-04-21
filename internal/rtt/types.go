package rtt

type tokenResponse struct {
	Token      string `json:"token"`
	ValidUntil string `json:"validUntil"`
}

type searchResponse struct {
	Services []serviceInfo `json:"services"`
}

type serviceInfo struct {
	ScheduleMetadata scheduleMetadata `json:"scheduleMetadata"`
	TemporalData     temporalData     `json:"temporalData"`
	LocationMetadata locationMetadata `json:"locationMetadata"`
	Destination      []locationRef    `json:"destination"`
}

type scheduleMetadata struct {
	UniqueIdentity     string   `json:"uniqueIdentity"`
	Identity           string   `json:"identity"`
	Operator           operator `json:"operator"`
	InPassengerService bool     `json:"inPassengerService"`
}

type operator struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

type temporalData struct {
	Departure *individualTemporal `json:"departure"`
	DisplayAs string              `json:"displayAs"`
}

type individualTemporal struct {
	ScheduleAdvertised string `json:"scheduleAdvertised"`
	IsCancelled        bool   `json:"isCancelled"`
}

type locationMetadata struct {
	Platform *platformInfo `json:"platform"`
}

type platformInfo struct {
	Planned string `json:"planned"`
	Actual  string `json:"actual"`
}

type locationRef struct {
	Location locationDetail `json:"location"`
}

type locationDetail struct {
	Description string   `json:"description"`
	ShortCodes  []string `json:"shortCodes"`
}
