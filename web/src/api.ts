export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
  }
}

async function request(method: string, path: string, body?: unknown): Promise<any> {
  const resp = await fetch(path, {
    method,
    headers: body !== undefined ? { 'Content-Type': 'application/json' } : {},
    body: body !== undefined ? JSON.stringify(body) : undefined,
    credentials: 'same-origin',
  })
  let data: any = null
  try {
    data = await resp.json()
  } catch {
    /* non-json */
  }
  if (!resp.ok) {
    throw new ApiError(resp.status, data?.error ?? resp.statusText)
  }
  return data
}

export const api = {
  get: (path: string) => request('GET', path),
  post: (path: string, body?: unknown) => request('POST', path, body),
  patch: (path: string, body?: unknown) => request('PATCH', path, body),
  del: (path: string) => request('DELETE', path),
}