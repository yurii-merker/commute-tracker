package darwin

import "encoding/xml"

type depBoardEnvelope struct {
	XMLName xml.Name     `xml:"Envelope"`
	Body    depBoardBody `xml:"Body"`
}

type depBoardBody struct {
	Response depBoardResponse `xml:"GetDepBoardWithDetailsResponse"`
}

type depBoardResponse struct {
	Result stationBoardResult `xml:"GetStationBoardResult"`
}

type stationBoardResult struct {
	TrainServices trainServices `xml:"trainServices"`
}

type trainServices struct {
	Services []serviceXML `xml:"service"`
}

type serviceXML struct {
	STD         string         `xml:"std"`
	ETD         string         `xml:"etd"`
	Platform    string         `xml:"platform"`
	ServiceID   string         `xml:"serviceID"`
	IsCancelled bool           `xml:"isCancelled"`
	Destination destinationXML `xml:"destination"`
}

type destinationXML struct {
	Locations []locationXML `xml:"location"`
}

type locationXML struct {
	Name string `xml:"locationName"`
}

type serviceDetailsEnvelope struct {
	XMLName xml.Name           `xml:"Envelope"`
	Body    serviceDetailsBody `xml:"Body"`
}

type serviceDetailsBody struct {
	Response serviceDetailsResponse `xml:"GetServiceDetailsResponse"`
}

type serviceDetailsResponse struct {
	Result serviceXML `xml:"GetServiceDetailsResult"`
}
