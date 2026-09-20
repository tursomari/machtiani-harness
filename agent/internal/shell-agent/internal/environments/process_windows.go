package environments

import (
	"fmt"
	"golang.org/x/sys/windows"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

type shellJob struct{ command, temporary windows.Handle }

var shellJobs = struct {
	sync.Mutex
	handles map[int]shellJob
}{handles: make(map[int]shellJob)}

func configureProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED | windows.CREATE_NEW_PROCESS_GROUP}
}
func startProcessGroup(cmd *exec.Cmd) (int, error) {
	commandJob, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	keepCommandJob := false
	defer func() {
		if !keepCommandJob {
			windows.CloseHandle(commandJob)
		}
	}()
	commandLimits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	commandLimits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(commandJob, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&commandLimits)), uint32(unsafe.Sizeof(commandLimits))); err != nil {
		return 0, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return 0, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	// Explicit asynchronous workers may leave this command's temporary job,
	// while the enclosing command job still owns their cancellation.
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE | windows.JOB_OBJECT_LIMIT_BREAKAWAY_OK
	if _, err = windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	if err = cmd.Start(); err != nil {
		windows.CloseHandle(job)
		return 0, err
	}
	fail := func(err error) (int, error) {
		cmd.Process.Kill()
		cmd.Wait()
		windows.CloseHandle(job)
		return 0, err
	}
	proc, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(cmd.Process.Pid))
	if err != nil {
		return fail(err)
	}
	err = windows.AssignProcessToJobObject(commandJob, proc)
	if err == nil {
		err = windows.AssignProcessToJobObject(job, proc)
	}
	windows.CloseHandle(proc)
	if err != nil {
		return fail(err)
	}
	// The child is suspended until job ownership is established, so it cannot
	// launch escaping descendants between Start and AssignProcessToJobObject.
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPTHREAD, 0)
	if err != nil {
		return fail(err)
	}
	entry := windows.ThreadEntry32{Size: uint32(unsafe.Sizeof(windows.ThreadEntry32{}))}
	err = windows.Thread32First(snapshot, &entry)
	resumed := false
	for err == nil {
		if entry.OwnerProcessID == uint32(cmd.Process.Pid) {
			thread, e := windows.OpenThread(windows.THREAD_SUSPEND_RESUME, false, entry.ThreadID)
			if e != nil {
				err = e
				break
			}
			_, e = windows.ResumeThread(thread)
			windows.CloseHandle(thread)
			err = e
			resumed = e == nil
			break
		}
		err = windows.Thread32Next(snapshot, &entry)
	}
	windows.CloseHandle(snapshot)
	if !resumed {
		return fail(fmt.Errorf("resume supervised child: %w", err))
	}

	shellJobs.Lock()
	shellJobs.handles[cmd.Process.Pid] = shellJob{command: commandJob, temporary: job}
	keepCommandJob = true
	shellJobs.Unlock()
	// Watch the process handle separately from exec.Cmd.Wait: descendants can
	// inherit stdout pipes and otherwise keep Wait blocked after their parent exits.
	waiter, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(cmd.Process.Pid))
	if err != nil {
		releaseProcessGroup(cmd, cmd.Process.Pid)
		cmd.Wait()
		return 0, err
	}
	go func() {
		defer windows.CloseHandle(waiter)
		_, _ = windows.WaitForSingleObject(waiter, windows.INFINITE)
		shellJobs.Lock()
		defer shellJobs.Unlock()
		if current, ok := shellJobs.handles[cmd.Process.Pid]; ok && current.temporary == job {
			_ = windows.TerminateJobObject(job, 1)
		}
	}()
	return cmd.Process.Pid, nil
}
func processGroupID(cmd *exec.Cmd) int { return cmd.Process.Pid }
func releaseProcessGroup(cmd *exec.Cmd, _ int) {
	shellJobs.Lock()
	defer shellJobs.Unlock()
	if job, ok := shellJobs.handles[cmd.Process.Pid]; ok {
		// Normal shell completion releases only the temporary job. An explicit
		// asynchronous worker retains command ownership until it exits. Closing
		// this process still closes the command handle and kills its workers.
		windows.CloseHandle(job.temporary)
		go releaseEmptyCommandJob(job.command)
		delete(shellJobs.handles, cmd.Process.Pid)
	}
}
func killProcessGroup(cmd *exec.Cmd, _ int) error {
	shellJobs.Lock()
	defer shellJobs.Unlock()
	if job, ok := shellJobs.handles[cmd.Process.Pid]; ok {
		return windows.TerminateJobObject(job.command, 1)
	}
	return nil
}

// JOBOBJECT_BASIC_ACCOUNTING_INFORMATION has fixed-width fields on Windows.
// Keep the outer job handle until all explicitly detached children have exited.
func releaseEmptyCommandJob(job windows.Handle) {
	defer windows.CloseHandle(job)
	var accounting struct {
		TotalUserTime, TotalKernelTime, PeriodUserTime, PeriodKernelTime     int64
		PageFaultCount, TotalProcesses, ActiveProcesses, TerminatedProcesses uint32
	}
	for {
		if err := windows.QueryInformationJobObject(job, windows.JobObjectBasicAccountingInformation, uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil); err != nil || accounting.ActiveProcesses == 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}
