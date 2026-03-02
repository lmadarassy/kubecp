import { Component } from '@angular/core';
import { RouterOutlet } from '@angular/router';
import { NavComponent } from './nav.component';

@Component({
  selector: 'app-layout',
  standalone: true,
  imports: [RouterOutlet, NavComponent],
  template: `
    <app-nav></app-nav>
    <main class="main-content">
      <router-outlet></router-outlet>
    </main>
  `,
  styles: [`
    .main-content {
      margin-left: 220px;
      padding: 24px;
      min-height: 100vh;
    }
  `]
})
export class LayoutComponent {}
