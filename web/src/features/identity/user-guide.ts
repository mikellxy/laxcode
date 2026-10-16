const COOKIE_NAME = "laxcode_guide_seen";

export function hasSeenUserGuide(): boolean {
  return document.cookie.split(";").some((item) => item.trim() === `${COOKIE_NAME}=1`);
}

export function markUserGuideSeen(): void {
  document.cookie = [`${COOKIE_NAME}=1`, "Path=/", "Max-Age=31536000", "SameSite=Lax", location.protocol === "https:" ? "Secure" : ""].filter(Boolean).join("; ");
}
