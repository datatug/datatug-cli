package gauth

//import (
//	"reflect"
//	"testing"
//
//	tea "github.com/charmbracelet/bubbletea"
//)
//
//func TestNewServiceAccountsUI(t *testing.T) {
//	type args struct {
//		store           Store
//		serviceAccounts []ServiceAccountDbo
//	}
//	tests := []struct {
//		name      string
//		args      args
//		want      tea.Model
//		wantErr   bool
//		wantPanic string
//	}{
//		{
//			name:      "panics if store is nil",
//			wantPanic: `store is nil`,
//		},
//	}
//	for _, tt := range tests {
//		t.Run(tt.name, func(t *testing.T) {
//			if tt.wantPanic != "" {
//				defer func() {
//					if r := recover(); r == nil {
//						t.Errorf("NewServiceAccountsUI did not panic")
//					} else if r.(string) != tt.wantPanic {
//						t.Errorf("NewServiceAccountsUI panic with %v, want %s", r, tt.wantPanic)
//					}
//				}()
//				_, _ = NewServiceAccountsUI(tt.args.store, tt.args.serviceAccounts)
//			} else {
//				got, err := NewServiceAccountsUI(tt.args.store, tt.args.serviceAccounts)
//				if (err != nil) != tt.wantErr {
//					t.Errorf("NewServiceAccountsUI() error = %v, wantErr %v", err, tt.wantErr)
//					return
//				}
//				if !reflect.DeepEqual(got, tt.want) {
//					t.Errorf("NewServiceAccountsUI() got = %v, want %v", got, tt.want)
//				}
//			}
//		})
//	}
//}
