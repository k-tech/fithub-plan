// Where a `#123` reference in user content should point.
//
// Kept in one place so every page that renders Markdown agrees on the answer.
//
// Known limitation: this product numbers Issues and pull requests separately, so
// `#123` is ambiguous and is resolved as an Issue. Reference validation would
// need a lookup per reference, which is not worth a request per number; if the
// two ever share one sequence this resolver becomes exact for free.
export function issueHrefFor(team: string, repo: string) {
  const base = `/${encodeURIComponent(team)}/${encodeURIComponent(repo)}`
  return (number: number) => `${base}/issues/${number}`
}
