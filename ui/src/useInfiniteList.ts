import { useCallback, useEffect, useRef, useState } from "react";

/** One page of a cursor-paged list: the rows and the cursor for the next. */
export type Page<T> = { items: T[]; next_cursor: string | null };

/**
 * A list that grows as it is scrolled, ../loom's useInfiniteList: page one
 * whenever `resetKeys` change (a new search, a mutation), the page after it
 * whenever the sentinel scrolls into view.
 *
 * `fetchPage` gets the cursor of the page wanted, null for the first, and
 * answers the envelope the API sends. `resetKeys` is an effect dependency
 * list: any change empties the list and asks for page one again.
 */
export function useInfiniteList<T>(
  fetchPage: (cursor: string | null) => Promise<Page<T>>,
  resetKeys: ReadonlyArray<unknown>,
) {
  const [items, setItems] = useState<T[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [cursor, setCursor] = useState<string | null>(null);
  const [hasMore, setHasMore] = useState(false);
  const [failed, setFailed] = useState(false);

  // The latest fetchPage without making it a reset dependency: it is a fresh
  // closure each render and would otherwise reload on every one.
  const fetchPageRef = useRef(fetchPage);
  fetchPageRef.current = fetchPage;

  // A reset bumps the request number so a slower request from a previous
  // search cannot land its rows over the newer list.
  const requestSeq = useRef(0);

  useEffect(() => {
    const seq = ++requestSeq.current;
    setItems([]);
    setLoaded(false);
    setCursor(null);
    setHasMore(false);
    setFailed(false);
    setLoadingMore(true);
    fetchPageRef
      .current(null)
      .then((page) => {
        if (seq !== requestSeq.current) return;
        setItems(page.items);
        setCursor(page.next_cursor);
        setHasMore(page.next_cursor !== null);
        setLoaded(true);
      })
      .catch(() => {
        if (seq === requestSeq.current) setFailed(true);
      })
      .finally(() => {
        if (seq === requestSeq.current) setLoadingMore(false);
      });
    // resetKeys is the dependency list, by design.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, resetKeys);

  // Not after a failure: the sentinel is still in view when a page fails
  // to load, and without this guard the observer would ask for it again the
  // moment the effect below re-observes, a request per tick until the reader
  // scrolled away. The next reset (a search, a mutation) clears the failure.
  const loadMore = useCallback(() => {
    if (loadingMore || failed || !hasMore || cursor === null) return;
    const seq = requestSeq.current;
    setLoadingMore(true);
    fetchPageRef
      .current(cursor)
      .then((page) => {
        if (seq !== requestSeq.current) return;
        setItems((prev) => [...prev, ...page.items]);
        setCursor(page.next_cursor);
        setHasMore(page.next_cursor !== null);
      })
      .catch(() => {
        if (seq === requestSeq.current) setFailed(true);
      })
      .finally(() => {
        if (seq === requestSeq.current) setLoadingMore(false);
      });
  }, [cursor, failed, hasMore, loadingMore]);

  // One observer per sentinel node, routed through a ref so it always calls
  // the latest loadMore, which closes over the current cursor.
  const loadMoreRef = useRef(loadMore);
  loadMoreRef.current = loadMore;
  const observerRef = useRef<IntersectionObserver | null>(null);
  const nodeRef = useRef<HTMLElement | null>(null);

  const sentinelRef = useCallback((node: HTMLElement | null) => {
    observerRef.current?.disconnect();
    observerRef.current = null;
    nodeRef.current = node;
    if (node === null || typeof IntersectionObserver === "undefined") return;
    // rootMargin loads the next page a little before the sentinel is reached.
    observerRef.current = new IntersectionObserver(
      (entries) => {
        if (entries.some((entry) => entry.isIntersecting)) loadMoreRef.current();
      },
      { rootMargin: "300px" },
    );
    observerRef.current.observe(node);
  }, []);

  // After a page settles, watch the sentinel afresh: an IntersectionObserver
  // fires on a CHANGE of visibility, so a sentinel still in view — a short
  // page that did not fill the viewport — would otherwise never load the
  // next page. Re-observing keeps filling until the viewport is covered or
  // there is nothing more.
  useEffect(() => {
    if (loadingMore || failed || !hasMore) return;
    const node = nodeRef.current;
    const observer = observerRef.current;
    if (node === null || observer === null) return;
    observer.unobserve(node);
    observer.observe(node);
  }, [loadingMore, failed, hasMore, items.length]);

  useEffect(() => () => observerRef.current?.disconnect(), []);

  return { items, setItems, loaded, loadingMore, hasMore, failed, sentinelRef, loadMore };
}
