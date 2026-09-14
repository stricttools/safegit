// Command safegit wraps git, giving each commit its own temporary index and
// retrying ref updates on conflict, so concurrent agents share one repository.
package main
