package sshdemo

// SlotEntry identifies the cgroup and credentials for a UID-isolated session child.
type SlotEntry struct {
	CgroupDir string
	UID       int
	GID       int
}

// SlotReadyFD is the first extra file passed by the supervisor. After successful
// slot entry, the child writes one byte and closes it.
const SlotReadyFD = 3
