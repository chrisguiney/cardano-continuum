/** getJSON fetches an API path; a non-2xx answer throws with the server's message. */
export async function getJSON<T>(path: string, signal?: AbortSignal): Promise<T> {
  const resp = await fetch(path, { signal, headers: { Accept: "application/json" } });
  if (!resp.ok) {
    const body = (await resp.json().catch(() => ({}))) as { error?: string };
    throw new Error(body.error ?? `HTTP ${resp.status}`);
  }
  return (await resp.json()) as T;
}
