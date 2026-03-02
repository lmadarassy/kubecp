import { Component, OnInit } from '@angular/core';
import { Router } from '@angular/router';
import { AuthService } from '../../services/auth.service';

@Component({
  selector: 'app-login',
  standalone: true,
  template: `
    <div class="login-container">
      <div class="login-card">
        <h2>Hosting Panel</h2>
        <p>Sign in with your account</p>
        <button class="primary" (click)="login()">Login with Keycloak</button>
      </div>
    </div>
  `,
  styles: [`
    .login-container {
      display: flex; justify-content: center; align-items: center;
      min-height: 100vh; background: #f5f6fa;
    }
    .login-card {
      background: #fff; padding: 40px; border-radius: 12px;
      box-shadow: 0 4px 12px rgba(0,0,0,0.1); text-align: center; width: 360px;
    }
    .login-card h2 { margin: 0 0 8px; }
    .login-card p { color: #6b7280; margin-bottom: 24px; }
    button.primary { width: 100%; padding: 12px; font-size: 15px; }
  `]
})
export class LoginComponent implements OnInit {
  constructor(private auth: AuthService, private router: Router) {}

  async ngOnInit(): Promise<void> {
    const loggedIn = await this.auth.tryAutoLogin();
    if (loggedIn) {
      this.router.navigate(['/']);
    }
  }

  async login(): Promise<void> {
    await this.auth.login();
  }
}
