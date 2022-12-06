package gcs

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/wal-g/tracelog"
	"golang.org/x/oauth2"
	auth "golang.org/x/oauth2/google"
)

// debug http requests
func debug(data []byte, err error) {
	if err == nil {
		tracelog.DebugLogger.Printf("%s\n\n", data)
	} else {
		log.Fatalf("%s\n\n", err)
	}
}

type InitiateUpload struct {
	XMLName  xml.Name `xml:"InitiateMultipartUploadResult"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xmml:"Key"`
	UploadId string   `xml:"UploadId"`
}

// Get default gcp credentials
func getGoogleToken() (*oauth2.Token, error) {
	var token *oauth2.Token
	ctx := context.Background()
	scopes := []string{
		"https://www.googleapis.com/auth/cloud-platform",
	}
	credentials, err := auth.FindDefaultCredentials(ctx, scopes...)
	if err == nil {
		token, err = credentials.TokenSource.Token()
		if err != nil {
			log.Print(err)
		}
		return token, nil
	}
	return nil, err
}

// get multipart xml UploadId
func getUploadId(token *oauth2.Token, bucketName string, objectName string) (string, error) {

	var xmlResponse InitiateUpload
	url := fmt.Sprintf("https://storage.googleapis.com/%s/%s?uploads", bucketName, objectName)

	req, err := http.NewRequest("POST", url, nil)
	if err != nil {
		tracelog.DebugLogger.Printf("creating request failed  : %v ", err.Error())
		return "", err
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	req.Header.Set("Date", time.Now().Format(time.RFC1123))
	req.Header.Set("Content-Length", "0")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		tracelog.DebugLogger.Printf("request failed  : %v ", err.Error())
		return "", err
	}
	body, _ := io.ReadAll(resp.Body)
	xml.Unmarshal(body, &xmlResponse)
	return xmlResponse.UploadId, nil
}

// Uploads a part of a multipart upload. Returns an ETag which must be used when completing the multipart upload.
// To ensure that data is not corrupted, you should specify a Content-MD5 header or a x-goog-hash header
// in the request so that Cloud Storage can check the uploaded data against the provided value.
// PUT /OBJECT_NAME?partNumber=PART_NUMBER&uploadId=UPLOAD_ID H
func uploadPart(token *oauth2.Token, bucketName string, objectName string, uploadId string, partNumber int, data []byte) (part *Part, e error) {

	tracelog.DebugLogger.Printf("Uploading part: %d of uploadID: %s\n", partNumber, uploadId)

	url := fmt.Sprintf("https://%s.storage.googleapis.com/%s?partNumber=%d&uploadId=%s", bucketName, objectName, partNumber, uploadId)

	req, err := http.NewRequest("PUT", url, bytes.NewBuffer(data))

	if err != nil {
		tracelog.DebugLogger.Printf("creating request failed  : %v ", err.Error())
		return nil, err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	req.Header.Set("Date", time.Now().Format(time.RFC1123))
	req.Header.Set("Content-Length", strconv.Itoa(len(data)))

	resp, err := http.DefaultClient.Do(req)

	if err != nil {
		return nil, e
	}

	if resp.Status[0] != '2' {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("expected status 2xx, got %s (%s)", resp.Status, string(b))
	}
	mypart := &Part{ETag: resp.Header.Get("Etag"), PartNumber: partNumber}
	return mypart, nil
}

// response when upload completed
type CompleteMultipartUploadResult struct {
	XMLName  xml.Name `xml:"CompleteMultipartUploadResult"`
	Location string   `xml:"Location"`
	Bucket   string   `xml:"Bucket"`
	Key      string   `xml:"Key"`
	ETag     string   `xml:"ETag"`
}

// info chunk uploaded
type Part struct {
	PartNumber int
	ETag       string
}

// send to complete request
type CompleteMultipartUpload struct {
	XMLName xml.Name `xml:"CompleteMultipartUpload"`
	Parts   []*Part  `xml:"Part"`
}

func completeUpload(token *oauth2.Token, parts []*Part, bucketName, objectName, uploadId string) (string, error) {

	for _, p := range parts {
		tracelog.DebugLogger.Printf("\nPART: %s, %d\n", p.ETag, p.PartNumber)
	}
	tracelog.DebugLogger.Println("Completing Upload")
	payload := &CompleteMultipartUpload{Parts: parts}
	buf := &bytes.Buffer{}
	e := xml.NewEncoder(buf).Encode(payload)
	//hackish maybe not needed to keep double quote and not encode them
	buf2 := bytes.Replace(buf.Bytes(), []byte("&#34;"), []byte("\""), -1)
	tracelog.DebugLogger.Printf("uploading completion summary:\n %v\n\n", string(buf2))

	if e != nil {
		log.Fatalf("error while completing upload : \n %v", e)
	}

	url := fmt.Sprintf("https://%s.storage.googleapis.com/%s?uploadId=%s", bucketName, objectName, uploadId)

	req, err := http.NewRequest("POST", url, bytes.NewReader(buf2))
	if err != nil {
		log.Fatalf("error while completing upload : \n %v", e)
	}
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token.AccessToken))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Content-Length", strconv.Itoa(buf.Len()))
	req.Header.Set("Date", time.Now().Format(time.RFC1123))
	debug(httputil.DumpRequest(req, true))

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Fatalf("error while completing upload : \n %v", e)
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(resp.Body)
	if e != nil {
		return "", e
	}

	tracelog.DebugLogger.Printf("\nResponse Code:%v\n", resp.StatusCode)
	tracelog.DebugLogger.Printf("\nResponse body::\n%v\n", string(b))
	// Heure de Verite
	result := &CompleteMultipartUploadResult{}
	e = xml.Unmarshal(b, result)
	if e != nil {
		tracelog.DebugLogger.Printf("%v", e)
		return "", e
	}

	tracelog.DebugLogger.Printf("completeUpload response:\n %v\n", result)
	return result.ETag, nil
}

func UploadToBucket(objectName string, objectContent io.Reader) {
	//TODO
	chunkSize := 10000000
	partNumber := 0
	parts := []*Part{}

	token, err := getGoogleToken()
	if err != nil {
		log.Fatalf("unable to get Google authentication token : %v ", err.Error())
	}

	bucketURL, err := url.Parse(os.Getenv("WALG_GS_PREFIX"))
	if err != nil {
		log.Fatal(err)
	}
	bucketName := bucketURL.Hostname()
	if len(bucketName) == 0 {
		log.Fatalf("no bucket found , is WALG_GS_BUCKET set ? value was = %s \n", os.Getenv("WALG_GS_PREFIX"))
	}
	tracelog.DebugLogger.Printf("\n\nARDBG:  bucketname = %s, object path = %s\n", bucketName, objectName)
	// get uploadId
	uploadId, err := getUploadId(token, bucketName, objectName)
	if err != nil {
		log.Fatalf("%v", err)
	}
	tracelog.DebugLogger.Printf("uploadId = %s\n", uploadId)
	//iterate through chunks
	for {
		buf := bytes.NewBuffer(make([]byte, 0, chunkSize))
		i, e := io.CopyN(buf, objectContent, int64(chunkSize))

		if e != nil && e != io.EOF {
			log.Fatalf("%v", e)
		}
		if i > 0 {
			partNumber++
			p, e := uploadPart(token, bucketName, objectName, uploadId, partNumber, buf.Bytes())
			if e != nil && e != io.EOF {
				log.Fatalf("%v", e)
			}
			parts = append(parts, p)
		}
		if e == io.EOF {
			break
		}
	}
	// close upload
	completeUpload(token, parts, bucketName, objectName, uploadId)
}
