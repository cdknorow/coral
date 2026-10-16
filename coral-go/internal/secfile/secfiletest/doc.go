// Package secfiletest helps the tests of the packages that write secret files (#3905). Its
// AssertPrivate, AssertOwnerOnly, Expose and OwnerOnlyDir work on every OS (#3920 T1): the mode
// bits on Unix, the file's access list on Windows (where the mode says nothing); the other
// helpers are Windows only.
package secfiletest
