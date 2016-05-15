package netlink

/*
#include <linux/taskstats.h>
*/
import "C"

type TaskStats C.struct_taskstats

const (
	__TASKSTATS_CMD_MAX = C.__TASKSTATS_CMD_MAX
)
