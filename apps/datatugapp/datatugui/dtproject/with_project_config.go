package dtproject

import (
	"github.com/datatug/datatug-core/pkg/dtconfig"
)

type WithProjectConfig interface {
	GetProjectConfig() *dtconfig.ProjectRef
}

//type withProjectConfig struct {
//	projectConfig *dtconfig.ProjectRef
//}
//
//func (v withProjectConfig) GetProjectConfig() *dtconfig.ProjectRef {
//	return v.projectConfig
//}
