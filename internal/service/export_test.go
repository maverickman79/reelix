package service

// SetBatchForTest overrides how many items one metadata refresh claims per
// query.
//
// It exists for one test, and that test is the reason this hook is worth
// having. The bug it pins — a full refresh that re-selected its first batch
// forever and could never reach the item after it — is INVISIBLE at any
// library size below the batch, and the real batch is two hundred films. A
// test built on a realistic six-film library passes against the broken code
// and against the fixed code alike, which is exactly the shape of test that
// let this survive. Shrinking the batch crosses the same boundary for the same
// reason at a size a test can actually build.
//
// In export_test.go rather than on the service, so it is compiled into the
// test binary only and can never be called by the server.
func (s *MetadataService) SetBatchForTest(n int) { s.batch = n }
