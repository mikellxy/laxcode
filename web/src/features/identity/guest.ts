const COOKIE_NAME = "laxcode_guest_id";

export function getOrCreateGuestID(): string {
  const prefix = `${COOKIE_NAME}=`;
  const stored = document.cookie.split("; ").find((item) => item.startsWith(prefix))?.slice(prefix.length);
  if (stored) return decodeURIComponent(stored);
  const id = crypto.randomUUID();
  document.cookie = [`${COOKIE_NAME}=${encodeURIComponent(id)}`, "Path=/", "Max-Age=31536000", "SameSite=Lax", location.protocol === "https:" ? "Secure" : ""].filter(Boolean).join("; ");
  return id;
}
