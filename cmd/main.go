async function fetchAPI(endpoint: string, options?: RequestInit) {
  try {
    const res = await fetch(`${API_URL}${endpoint}`, {
      headers: { 'Content-Type': 'application/json', ...options?.headers },
      ...options,
    })
    if (!res.ok) {
      const error = await res.json().catch(() => ({ error: 'Request failed' }))
      throw new Error(error.error || 'Request failed')
    }
    const json = await res.json()
    if (json.data !== undefined) {
      return json.data
    }
    return json
  } catch (error) {
    console.error(`API Error [${endpoint}]:`, error)
    return null
  }
}

export function formatPrice(price: number | undefined | null): string {
  if (price === undefined || price === null) return '0,00 €'
  return new Intl.NumberFormat('sk-SK', { style: 'currency', currency: 'EUR' }).format(price)
}

export function formatDate(date: string | Date): string {
  return new Intl.DateTimeFormat('sk-SK', {
    day: '2-digit', month: '2-digit', year: 'numeric', hour: '2-digit', minute: '2-digit',
  }).format(new Date(date))
}

export const api = {
  async getProducts(params?: { page?: number; limit?: number; search?: string; category?: string; brand?: string; min_price?: number; max_price?: number; sort?: string }) {
    const query = new URLSearchParams()
    if (params?.page) query.set('page', String(params.page))
    if (params?.limit) query.set('limit', String(params.limit))
    if (params?.search) query.set('search', params.search)
    if (params?.category) query.set('category', params.category)
    if (params?.brand) query.set('brand', params.brand)
    if (params?.min_price) query.set('min_price', String(params.min_price))
    if (params?.max_price) query.set('max_price', String(params.max_price))
    if (params?.sort) query.set('sort', params.sort)
    return fetchAPI(`/products?${query}`)
  },
  async getProductBySlug(slug: string) { return fetchAPI(`/products/slug/${slug}`) },
  async getProductOffers(productId: string) { return fetchAPI(`/products/${productId}/offers`) },
  async getProductsByCategory(slug: string, params?: { page?: number; limit?: number }) {
    const query = new URLSearchParams({ category: slug })
    if (params?.page) query.set('page', String(params.page))
    if (params?.limit) query.set('limit', String(params.limit))
    return fetchAPI(`/products?${query}`)
  },
  async getFeaturedProducts(limit: number = 8) {
    const data = await fetchAPI(`/products?limit=${limit}&sort=newest`)
    return data?.items || data || []
  },
  async search(query: string, params?: { page?: number; limit?: number }) {
    const searchParams = new URLSearchParams({ search: query })
    if (params?.page) searchParams.set('page', String(params.page))
    if (params?.limit) searchParams.set('limit', String(params.limit))
    return fetchAPI(`/search?${searchParams}`)
  },
  async getCategories() { return fetchAPI('/categories') },
  async getCategoryTree() { return fetchAPI('/categories/tree') },
  async getCategoriesTree() { return fetchAPI('/categories/tree') },
  async getCategoriesFlat() { return fetchAPI('/categories/flat') },
  async getCategoryBySlug(slug: string) { return fetchAPI(`/categories/slug/${slug}`) },
  async getAdminProducts(params?: { page?: number; limit?: number; search?: string; category?: string; status?: string }) {
    const query = new URLSearchParams()
    if (params?.page) query.set('page', String(params.page))
    if (params?.limit) query.set('limit', String(params.limit))
    if (params?.search) query.set('search', params.search)
    if (params?.category) query.set('category', params.category)
    if (params?.status) query.set('status', params.status)
    return fetchAPI(`/admin/products?${query}`)
  },
  async getProduct(id: string) { return fetchAPI(`/admin/products/${id}`) },
  async createProduct(data: any) { return fetchAPI('/admin/products', { method: 'POST', body: JSON.stringify(data) }) },
  async updateProduct(id: string, data: any) { return fetchAPI(`/admin/products/${id}`, { method: 'PUT', body: JSON.stringify(data) }) },
  async deleteProduct(id: string) { return fetchAPI(`/admin/products/${id}`, { method: 'DELETE' }) },
  async bulkUpdateProducts(ids: string[], action: 'activate' | 'deactivate' | 'delete') {
    return fetchAPI('/admin/products/bulk', { method: 'POST', body: JSON.stringify({ ids, action }) })
  },
  async syncProductsToES() { return fetchAPI('/admin/products/sync-es', { method: 'POST' }) },
  async getCategory(id: string) { return fetchAPI(`/admin/categories/${id}`) },
  async createCategory(data: any) { return fetchAPI('/admin/categories', { method: 'POST', body: JSON.stringify(data) }) },
  async updateCategory(id: string, data: any) { return fetchAPI(`/admin/categories/${id}`, { method: 'PUT', body: JSON.stringify(data) }) },
  async deleteCategory(id: string) { return fetchAPI(`/admin/categories/${id}`, { method: 'DELETE' }) },
  async getFeeds() { return fetchAPI('/admin/feeds') },
  async createFeed(data: any) { return fetchAPI('/admin/feeds', { method: 'POST', body: JSON.stringify(data) }) },
  async previewFeed(url: string) { return fetchAPI('/admin/feeds/preview', { method: 'POST', body: JSON.stringify({ url }) }) },
  async updateFeed(id: string, data: any) { return fetchAPI(`/admin/feeds/${id}`, { method: 'PUT', body: JSON.stringify(data) }) },
  async deleteFeed(id: string) { return fetchAPI(`/admin/feeds/${id}`, { method: 'DELETE' }) },
  async startFeedImport(id: string) { return fetchAPI(`/admin/feeds/${id}/import`, { method: 'POST' }) },
  async getFeedProgress(id: string) { return fetchAPI(`/admin/feeds/${id}/progress`) },
  async uploadImage(file: File) {
    const formData = new FormData()
    formData.append('file', file)
    try {
      const res = await fetch(`${API_URL}/admin/upload`, { method: 'POST', body: formData })
      const json = await res.json()
      return json.data || json
    } catch (error) { return null }
  },
  async uploadMultipleImages(files: File[]) {
    const formData = new FormData()
    files.forEach(file => formData.append('files', file))
    try {
      const res = await fetch(`${API_URL}/admin/upload/multiple`, { method: 'POST', body: formData })
      const json = await res.json()
      return json.data || json
    } catch (error) { return null }
  },
}

export default api