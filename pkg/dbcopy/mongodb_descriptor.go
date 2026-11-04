package dbcopy

import "os"

// ReadMongoDBDescriptor reads and strictly decodes the MongoDB connection
// descriptor at path, applying the same dsnEnv rule as the other descriptor
// readers: a project file may name only a variable the operator allowed, and
// the connection URI (credentials included) is never in the project.
func ReadMongoDBDescriptor(path string) (ConnectionDescriptor, error) {
	return readConnectionDescriptor(path, "MongoDB", os.LookupEnv)
}
