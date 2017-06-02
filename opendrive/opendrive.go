package opendrive

import (
	"io"
	"net/http"
	"time"

	"fmt"

	"github.com/ncw/rclone/dircache"
	"github.com/ncw/rclone/fs"
	"github.com/ncw/rclone/pacer"
	"github.com/ncw/rclone/rest"
	"github.com/pkg/errors"
)

const (
	defaultEndpoint = "https://dev.opendrive.com/api/v1"
	minSleep        = 10 * time.Millisecond
	maxSleep        = 5 * time.Minute
	decayConstant   = 1 // bigger for slower decay, exponential
	maxParts        = 10000
	maxVersions     = 100 // maximum number of versions we search in --b2-versions mode
)

// Register with Fs
func init() {
	fs.Register(&fs.RegInfo{
		Name:        "opendrive",
		Description: "OpenDRIVE",
		NewFs:       NewFs,
		Options: []fs.Option{{
			Name: "username",
			Help: "Username",
		}, {
			Name:       "password",
			Help:       "Password.",
			IsPassword: true,
		}},
	})
}

// Fs represents a remote b2 server
type Fs struct {
	name     string             // name of this remote
	features *fs.Features       // optional features
	username string             // account name
	password string             // auth key0
	srv      *rest.Client       // the connection to the b2 server
	pacer    *pacer.Pacer       // To pace and retry the API calls
	session  UserSessionInfo    // contains the session data
	dirCache *dircache.DirCache // Map of directory path to directory id
}

// Object describes a b2 object
type Object struct {
	fs      *Fs       // what this object is part of
	remote  string    // The remote path
	id      string    // b2 id of the file
	modTime time.Time // The modified time of the object if known
	md5     string    // MD5 hash if known
	size    int64     // Size of the object
}

// ------------------------------------------------------------

// Name of the remote (as passed into NewFs)
func (f *Fs) Name() string {
	return f.name
}

// Root of the remote (as passed into NewFs)
func (f *Fs) Root() string {
	return "/"
}

// String converts this Fs to a string
func (f *Fs) String() string {
	return "OpenDRIVE"
}

// Features returns the optional features of this Fs
func (f *Fs) Features() *fs.Features {
	return f.features
}

// Hashes returns the supported hash sets.
func (f *Fs) Hashes() fs.HashSet {
	return fs.HashSet(fs.HashMD5)
}

// List walks the path returning iles and directories into out
func (f *Fs) List(out fs.ListOpts, dir string) {
	f.dirCache.List(f, out, dir)
}

// NewFs contstructs an Fs from the path, bucket:path
func NewFs(name, root string) (fs.Fs, error) {
	username := fs.ConfigFileGet(name, "username")
	if username == "" {
		return nil, errors.New("username not found")
	}
	password, err := fs.Reveal(fs.ConfigFileGet(name, "password"))
	if err != nil {
		return nil, errors.New("password coudl not revealed")
	}
	if password == "" {
		return nil, errors.New("password not found")
	}

	fs.Debugf(nil, "OpenDRIVE-user: %s", username)
	fs.Debugf(nil, "OpenDRIVE-pass: %s", password)

	f := &Fs{
		name:     name,
		username: username,
		password: password,
		srv:      rest.NewClient(fs.Config.Client()).SetErrorHandler(errorHandler),
		pacer:    pacer.New().SetMinSleep(minSleep).SetMaxSleep(maxSleep).SetDecayConstant(decayConstant),
	}

	f.dirCache = dircache.New(root, "0", f)

	// set the rootURL for the REST client
	f.srv.SetRoot(defaultEndpoint)

	// get sessionID
	var resp *http.Response
	err = f.pacer.Call(func() (bool, error) {
		account := Account{Username: username, Password: password}

		opts := rest.Opts{
			Method: "POST",
			Path:   "/session/login.json",
		}
		resp, err = f.srv.CallJSON(&opts, &account, &f.session)
		return f.shouldRetry(resp, err)
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to create session")
	}

	fs.Debugf(nil, "Starting OpenDRIVE session with ID: %s", f.session.SessionID)

	// f.features = (&fs.Features{ReadMimeType: true, WriteMimeType: true}).Fill(f)
	// // Set the test flag if required
	// if *b2TestMode != "" {
	// 	testMode := strings.TrimSpace(*b2TestMode)
	// 	f.srv.SetHeader(testModeHeader, testMode)
	// 	fs.Debugf(f, "Setting test header \"%s: %s\"", testModeHeader, testMode)
	// }
	// // Fill up the buffer tokens
	// for i := 0; i < fs.Config.Transfers; i++ {
	// 	f.bufferTokens <- nil
	// }
	// err = f.authorizeAccount()
	// if err != nil {
	// 	return nil, errors.Wrap(err, "failed to authorize account")
	// }
	// if f.root != "" {
	// 	f.root += "/"
	// 	// Check to see if the (bucket,directory) is actually an existing file
	// 	oldRoot := f.root
	// 	remote := path.Base(directory)
	// 	f.root = path.Dir(directory)
	// 	if f.root == "." {
	// 		f.root = ""
	// 	} else {
	// 		f.root += "/"
	// 	}
	// 	_, err := f.NewObject(remote)
	// 	if err != nil {
	// 		if err == fs.ErrorObjectNotFound {
	// 			// File doesn't exist so return old f
	// 			f.root = oldRoot
	// 			return f, nil
	// 		}
	// 		return nil, err
	// 	}
	// 	// return an error with an fs which points to the parent
	// 	return f, fs.ErrorIsFile
	// }
	return f, nil
}

// errorHandler parses a non 2xx error response into an error
func errorHandler(resp *http.Response) error {
	// Decode error response
	// errResponse := new(api.Error)
	// err := rest.DecodeJSON(resp, &errResponse)
	// if err != nil {
	// 	fs.Debugf(nil, "Couldn't decode error response: %v", err)
	// }
	// if errResponse.Code == "" {
	// 	errResponse.Code = "unknown"
	// }
	// if errResponse.Status == 0 {
	// 	errResponse.Status = resp.StatusCode
	// }
	// if errResponse.Message == "" {
	// 	errResponse.Message = "Unknown " + resp.Status
	// }
	// return errResponse
	return nil
}

// Mkdir creates the bucket if it doesn't exist
func (f *Fs) Mkdir(dir string) error {
	// // Can't create subdirs
	// if dir != "" {
	// 	return nil
	// }
	// opts := rest.Opts{
	// 	Method: "POST",
	// 	Path:   "/b2_create_bucket",
	// }
	// var request = api.CreateBucketRequest{
	// 	AccountID: f.info.AccountID,
	// 	Name:      f.bucket,
	// 	Type:      "allPrivate",
	// }
	// var response api.Bucket
	// err := f.pacer.Call(func() (bool, error) {
	// 	resp, err := f.srv.CallJSON(&opts, &request, &response)
	// 	return f.shouldRetry(resp, err)
	// })
	// if err != nil {
	// 	if apiErr, ok := err.(*api.Error); ok {
	// 		if apiErr.Code == "duplicate_bucket_name" {
	// 			// Check this is our bucket - buckets are globally unique and this
	// 			// might be someone elses.
	// 			_, getBucketErr := f.getBucketID()
	// 			if getBucketErr == nil {
	// 				// found so it is our bucket
	// 				return nil
	// 			}
	// 			if getBucketErr != fs.ErrorDirNotFound {
	// 				fs.Debugf(f, "Error checking bucket exists: %v", getBucketErr)
	// 			}
	// 		}
	// 	}
	// 	return errors.Wrap(err, "failed to create bucket")
	// }
	// f.setBucketID(response.ID)
	return nil
}

// Rmdir deletes the bucket if the fs is at the root
//
// Returns an error if it isn't empty
func (f *Fs) Rmdir(dir string) error {
	// if f.root != "" || dir != "" {
	// 	return nil
	// }
	// opts := rest.Opts{
	// 	Method: "POST",
	// 	Path:   "/b2_delete_bucket",
	// }
	// bucketID, err := f.getBucketID()
	// if err != nil {
	// 	return err
	// }
	// var request = api.DeleteBucketRequest{
	// 	ID:        bucketID,
	// 	AccountID: f.info.AccountID,
	// }
	// var response api.Bucket
	// err = f.pacer.Call(func() (bool, error) {
	// 	resp, err := f.srv.CallJSON(&opts, &request, &response)
	// 	return f.shouldRetry(resp, err)
	// })
	// if err != nil {
	// 	return errors.Wrap(err, "failed to delete bucket")
	// }
	// f.clearBucketID()
	// f.clearUploadURL()
	return nil
}

// Precision of the remote
func (f *Fs) Precision() time.Duration {
	return time.Millisecond
}

// Return an Object from a path
//
// If it can't be found it returns the error fs.ErrorObjectNotFound.
func (f *Fs) newObjectWithInfo(remote string, file *File) (fs.Object, error) {
	fs.Debugf(nil, "newObjectWithInfo(%s, %v)", remote, file)
	o := &Object{
		fs:      f,
		remote:  remote,
		id:      file.FileID,
		modTime: time.Unix(file.DateModified, 0),
		size:    file.Size,
	}

	return o, nil
}

// NewObject finds the Object at remote.  If it can't be found
// it returns the error fs.ErrorObjectNotFound.
func (f *Fs) NewObject(remote string) (fs.Object, error) {
	return f.newObjectWithInfo(remote, nil)
}

// Put the object into the bucket
//
// Copy the reader in to the new object which is returned
//
// The new object may have been created if an error is returned
func (f *Fs) Put(in io.Reader, src fs.ObjectInfo) (fs.Object, error) {
	// Temporary Object under construction
	// fs := &Object{
	// 	fs:     f,
	// 	remote: src.Remote(),
	// }
	// return fs, fs.Update(in, src)
	return nil, nil
}

// retryErrorCodes is a slice of error codes that we will retry
var retryErrorCodes = []int{
	400, // Bad request (seen in "Next token is expired")
	401, // Unauthorized (seen in "Token has expired")
	408, // Request Timeout
	429, // Rate exceeded.
	500, // Get occasional 500 Internal Server Error
	502, // Bad Gateway when doing big listings
	503, // Service Unavailable
	504, // Gateway Time-out
}

// shouldRetry returns a boolean as to whether this resp and err
// deserve to be retried.  It returns the err as a convenience
func (f *Fs) shouldRetry(resp *http.Response, err error) (bool, error) {
	// if resp != nil {
	// 	if resp.StatusCode == 401 {
	// 		f.tokenRenewer.Invalidate()
	// 		fs.Debugf(f, "401 error received - invalidating token")
	// 		return true, err
	// 	}
	// 	// Work around receiving this error sporadically on authentication
	// 	//
	// 	// HTTP code 403: "403 Forbidden", reponse body: {"message":"Authorization header requires 'Credential' parameter. Authorization header requires 'Signature' parameter. Authorization header requires 'SignedHeaders' parameter. Authorization header requires existence of either a 'X-Amz-Date' or a 'Date' header. Authorization=Bearer"}
	// 	if resp.StatusCode == 403 && strings.Contains(err.Error(), "Authorization header requires") {
	// 		fs.Debugf(f, "403 \"Authorization header requires...\" error received - retry")
	// 		return true, err
	// 	}
	// }
	return fs.ShouldRetry(err) || fs.ShouldRetryHTTP(resp, retryErrorCodes), err
}

// DirCacher methos

// CreateDir makes a directory with pathID as parent and name leaf
func (f *Fs) CreateDir(pathID, leaf string) (newID string, err error) {
	fs.Debugf(nil, "CreateDir(\"%s\", \"%s\")", pathID, leaf)
	// //fmt.Printf("CreateDir(%q, %q)\n", pathID, leaf)
	// folder := acd.FolderFromId(pathID, f.c.Nodes)
	// var resp *http.Response
	// var info *acd.Folder
	// err = f.pacer.Call(func() (bool, error) {
	// 	info, resp, err = folder.CreateFolder(leaf)
	// 	return f.shouldRetry(resp, err)
	// })
	// if err != nil {
	// 	//fmt.Printf("...Error %v\n", err)
	// 	return "", err
	// }
	// //fmt.Printf("...Id %q\n", *info.Id)
	// return *info.Id, nil
	return "", fmt.Errorf("CreateDir not implemented")
}

// FindLeaf finds a directory of name leaf in the folder with ID pathID
func (f *Fs) FindLeaf(pathID, leaf string) (pathIDOut string, found bool, err error) {
	fs.Debugf(nil, "FindLeaf(\"%s\", \"%s\")", pathID, leaf)

	if pathID == "0" && leaf == "" {
		// that's the root directory
		return pathID, true, nil
	}

	// get the folderIDs
	var resp *http.Response
	folderList := FolderList{}
	err = f.pacer.Call(func() (bool, error) {
		opts := rest.Opts{
			Method: "GET",
			Path:   "/folder/list.json/" + f.session.SessionID + "/" + pathID,
		}
		resp, err = f.srv.CallJSON(&opts, nil, &folderList)
		return f.shouldRetry(resp, err)
	})
	if err != nil {
		return "", false, errors.Wrap(err, "failed to get folder list")
	}

	for _, folder := range folderList.Folders {
		fs.Debugf(nil, "Folder: %s (%s)", folder.Name, folder.FolderID)

		if leaf == folder.Name {
			// found
			return folder.FolderID, true, nil
		}
	}
	for _, file := range folderList.Files {
		fs.Debugf(nil, "File: %s (%s)", file.Name, file.FileID)
		if leaf == file.Name {
			// found
			return file.FileID, true, nil
		}
	}

	return "", false, nil
}

// ListDir reads the directory specified by the job into out, returning any more jobs
func (f *Fs) ListDir(out fs.ListOpts, job dircache.ListDirJob) (jobs []dircache.ListDirJob, err error) {
	fs.Debugf(nil, "ListDir(%v, %v)", out, job)
	// get the folderIDs
	var resp *http.Response
	folderList := FolderList{}
	err = f.pacer.Call(func() (bool, error) {
		opts := rest.Opts{
			Method: "GET",
			Path:   "/folder/list.json/" + f.session.SessionID + "/" + job.DirID,
		}
		resp, err = f.srv.CallJSON(&opts, nil, &folderList)
		return f.shouldRetry(resp, err)
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to get folder list")
	}

	for _, folder := range folderList.Folders {
		fs.Debugf(nil, "Folder: %s (%s)", folder.Name, folder.FolderID)
		remote := job.Path + folder.Name
		if out.IncludeDirectory(remote) {
			dir := &fs.Dir{
				Name:  remote,
				Bytes: -1,
				Count: -1,
			}
			dir.When = time.Unix(int64(folder.DateModified), 0)
			if out.AddDir(dir) {
				continue
			}
			if job.Depth > 0 {
				jobs = append(jobs, dircache.ListDirJob{DirID: folder.FolderID, Path: remote + "/", Depth: job.Depth - 1})
			}
		}
	}

	for _, file := range folderList.Files {
		fs.Debugf(nil, "File: %s (%s)", file.Name, file.FileID)
		remote := job.Path + file.Name
		o, err := f.newObjectWithInfo(remote, &file)
		if err != nil {
			out.SetError(err)
			continue
		}
		out.Add(o)
	}

	return jobs, nil
}

// ------------------------------------------------------------

// Fs returns the parent Fs
func (o *Object) Fs() fs.Info {
	return o.fs
}

// Return a string version
func (o *Object) String() string {
	if o == nil {
		return "<nil>"
	}
	return o.remote
}

// Remote returns the remote path
func (o *Object) Remote() string {
	return o.remote
}

// Hash returns the Md5sum of an object returning a lowercase hex string
func (o *Object) Hash(t fs.HashType) (string, error) {
	if t != fs.HashMD5 {
		return "", fs.ErrHashUnsupported
	}
	return o.md5, nil
}

// Size returns the size of an object in bytes
func (o *Object) Size() int64 {
	return o.size // Object is likely PENDING
}

// ModTime returns the modification time of the object
//
//
// It attempts to read the objects mtime and if that isn't present the
// LastModified returned in the http headers
func (o *Object) ModTime() time.Time {
	return o.modTime
}

// SetModTime sets the modification time of the local fs object
func (o *Object) SetModTime(modTime time.Time) error {
	// FIXME not implemented
	return fs.ErrorCantSetModTime
}

// Open an object for read
func (o *Object) Open(options ...fs.OpenOption) (in io.ReadCloser, err error) {
	// bigObject := o.Size() >= int64(tempLinkThreshold)
	// if bigObject {
	// 	fs.Debugf(o, "Downloading large object via tempLink")
	// }
	// file := acd.File{Node: o.info}
	// var resp *http.Response
	// headers := fs.OpenOptionHeaders(options)
	// err = o.fs.pacer.Call(func() (bool, error) {
	// 	if !bigObject {
	// 		in, resp, err = file.OpenHeaders(headers)
	// 	} else {
	// 		in, resp, err = file.OpenTempURLHeaders(rest.ClientWithHeaderReset(o.fs.noAuthClient, headers), headers)
	// 	}
	// 	return o.fs.shouldRetry(resp, err)
	// })
	// return in, err
	return nil, fmt.Errorf("Open not implemented")
}

// Remove an object
func (o *Object) Remove() error {
	return fmt.Errorf("Remove not implemented")
}

// Storable returns a boolean showing whether this object storable
func (o *Object) Storable() bool {
	return true
}

// Update the object with the contents of the io.Reader, modTime and size
//
// The new object may have been created if an error is returned
func (o *Object) Update(in io.Reader, src fs.ObjectInfo) error {
	// file := acd.File{Node: o.info}
	// var info *acd.File
	// var resp *http.Response
	// var err error
	// err = o.fs.pacer.CallNoRetry(func() (bool, error) {
	// 	start := time.Now()
	// 	o.fs.tokenRenewer.Start()
	// 	info, resp, err = file.Overwrite(in)
	// 	o.fs.tokenRenewer.Stop()
	// 	var ok bool
	// 	ok, info, err = o.fs.checkUpload(resp, in, src, info, err, time.Since(start))
	// 	if ok {
	// 		return false, nil
	// 	}
	// 	return o.fs.shouldRetry(resp, err)
	// })
	// if err != nil {
	// 	return err
	// }
	// o.info = info.Node
	// return nil

	return fmt.Errorf("Update not implemented")
}
