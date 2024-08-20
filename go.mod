module github.com/pkg/sftp

go 1.22.6

replace github.com/vmware/go-nfs-client => /home/nutanix/sftp-go/go-nfs-client/

require (
	github.com/kr/fs v0.1.0
	github.com/rdleal/intervalst v1.4.0
	github.com/stretchr/testify v1.9.0
	github.com/vmware/go-nfs-client v0.0.0-20190605212624-d43b92724c1b
	github.com/willscott/go-nfs-client v0.0.0-20240104095149-b44639837b00
	golang.org/x/crypto v0.26.0
)

require (
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
	github.com/rasky/go-xdr v0.0.0-20170124162913-1a41d1a06c93 // indirect
	golang.org/x/sys v0.23.0 // indirect
	gopkg.in/yaml.v3 v3.0.1 // indirect
)
