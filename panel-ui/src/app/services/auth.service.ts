import { Injectable } from '@angular/core';
import { OAuthService, AuthConfig } from 'angular-oauth2-oidc';
import { Router } from '@angular/router';

@Injectable({ providedIn: 'root' })
export class AuthService {
  private readonly IMPERSONATION_KEY = 'impersonated_user';

  constructor(private oauthService: OAuthService, private router: Router) {}

  configure(): void {
    // Keycloak is reverse-proxied on the same origin under /realms/...
    // This avoids cross-origin issues with self-signed certs.
    const issuerUrl = `${window.location.origin}/realms/hosting`;

    const config: AuthConfig = {
      issuer: issuerUrl,
      redirectUri: window.location.origin + '/callback',
      postLogoutRedirectUri: window.location.origin + '/login',
      clientId: 'panel-ui',
      responseType: 'code',
      scope: 'openid profile email roles',
      showDebugInformation: false,
      requireHttps: false, // allow http in dev
    };
    this.oauthService.configure(config);
    this.oauthService.setupAutomaticSilentRefresh();
  }

  async login(): Promise<void> {
    await this.oauthService.loadDiscoveryDocumentAndLogin();
  }

  logout(): void {
    this.oauthService.logOut();
  }

  get isAuthenticated(): boolean {
    return this.oauthService.hasValidAccessToken();
  }

  get accessToken(): string {
    return this.oauthService.getAccessToken();
  }

  get identityClaims(): Record<string, unknown> {
    return this.oauthService.getIdentityClaims() as Record<string, unknown> || {};
  }

  get username(): string {
    return (this.identityClaims['preferred_username'] as string) || '';
  }

  get roles(): string[] {
    // Try ID token claims first
    const claims = this.identityClaims;
    const realmAccess = claims['realm_access'] as { roles?: string[] } | undefined;
    if (realmAccess?.roles?.length) {
      return realmAccess.roles;
    }
    // Fallback: decode access token payload directly
    try {
      const token = this.accessToken;
      if (!token) return [];
      const payload = token.split('.')[1];
      const padded = payload + '=='.slice((payload.length + 2) % 4 || 4);
      const decoded = JSON.parse(atob(padded));
      return decoded?.realm_access?.roles || [];
    } catch {
      return [];
    }
  }

  get isAdmin(): boolean {
    return this.roles.includes('admin');
  }

  get impersonatedUser(): string | null {
    return sessionStorage.getItem(this.IMPERSONATION_KEY);
  }

  setImpersonatedUser(username: string | null): void {
    if (username) {
      sessionStorage.setItem(this.IMPERSONATION_KEY, username);
    } else {
      sessionStorage.removeItem(this.IMPERSONATION_KEY);
    }
  }

  async tryAutoLogin(): Promise<boolean> {
    try {
      await this.oauthService.loadDiscoveryDocumentAndTryLogin();
      return this.isAuthenticated;
    } catch {
      return false;
    }
  }
}
