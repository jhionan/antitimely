package osbridge

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Supervise runs the scheduled daemon in a job that terminates its children
// when the Task Scheduler stops the supervisor. Abnormal child exits use a
// bounded backoff, independent of how the task was started.
func Supervise(exe string, args []string, retry time.Duration) error {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return fmt.Errorf("create tracker job: %w", err)
	}
	defer windows.CloseHandle(job)
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return fmt.Errorf("configure tracker job: %w", err)
	}
	delay := retry
	for {
		started := time.Now()
		code, err := runJobProcess(job, exe, args)
		if err != nil {
			return err
		}
		if code == 0 {
			return nil
		}
		if time.Since(started) >= time.Minute {
			delay = retry
		}
		time.Sleep(delay)
		delay = min(2*delay, time.Minute)
	}
}

func runJobProcess(job windows.Handle, exe string, args []string) (uint32, error) {
	const jobListAttribute = 0x0002000d // PROC_THREAD_ATTRIBUTE_JOB_LIST, Windows 10+
	attributes, err := windows.NewProcThreadAttributeList(1)
	if err != nil {
		return 0, err
	}
	defer attributes.Delete()
	// Assign the job as part of process creation. This closes the gap where
	// stopping the task could leave a child alive before it joins the job.
	if err := attributes.Update(jobListAttribute, unsafe.Pointer(&job), unsafe.Sizeof(job)); err != nil {
		return 0, fmt.Errorf("attach tracker job attribute: %w", err)
	}
	executable, err := windows.UTF16PtrFromString(exe)
	if err != nil {
		return 0, err
	}
	command, err := windows.UTF16PtrFromString(windows.ComposeCommandLine(append([]string{exe}, args...)))
	if err != nil {
		return 0, err
	}
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attributes.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	var child windows.ProcessInformation
	flags := uint32(windows.EXTENDED_STARTUPINFO_PRESENT | windows.CREATE_NO_WINDOW)
	if err := windows.CreateProcess(executable, command, nil, nil, false, flags,
		nil, nil, &startup.StartupInfo, &child); err != nil {
		return 0, fmt.Errorf("start tracker process: %w", err)
	}
	windows.CloseHandle(child.Thread)
	defer windows.CloseHandle(child.Process)
	if _, err := windows.WaitForSingleObject(child.Process, windows.INFINITE); err != nil {
		return 0, err
	}
	var code uint32
	err = windows.GetExitCodeProcess(child.Process, &code)
	return code, err
}
