package configfiles

import (
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"unsafe"
)

func currentSID() (*windows.SID, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return user.User.Sid, nil
}
func protectPrivate(path string) error {
	sid, err := currentSID()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;" + sid.String() + ")(A;;FA;;;SY)")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}
func openPrivate(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.READ_CONTROL, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(h), path)
	okay := false
	defer func() {
		if !okay {
			f.Close()
		}
	}()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DIRECTORY) != 0 {
		return nil, errors.New("credential file must be a regular file")
	}
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() > 1048576 {
		return nil, errors.New("credential file is too large")
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return nil, err
	}
	sid, err := currentSID()
	if err != nil {
		return nil, err
	}
	owner, _, err := sd.Owner()
	if err != nil {
		return nil, err
	}
	if !owner.Equals(sid) {
		return nil, errors.New("credential file must be owned by current user")
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return nil, err
	}
	if acl == nil {
		return nil, errors.New("credential file must have a private DACL")
	}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			return nil, err
		}
		if ace.Header.AceType == windows.ACCESS_DENIED_ACE_TYPE {
			continue
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			return nil, errors.New("unsupported credential ACL")
		}
		other := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if ace.Mask != 0 && !other.Equals(sid) && !other.IsWellKnown(windows.WinLocalSystemSid) {
			return nil, errors.New("credential file is accessible by another principal")
		}
	}
	okay = true
	return f, nil
}
