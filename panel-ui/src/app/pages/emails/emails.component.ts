import { Component, OnInit } from '@angular/core';
import { NgIf, NgFor, NgClass } from '@angular/common';
import { FormsModule } from '@angular/forms';
import { ApiService } from '../../services/api.service';

interface EmailDomain {
  id: string;
  domain: string;
  owner: string;
  spamFilter: boolean;
  catchAll?: string;
  accountCount: number;
  phase?: string;
}

interface EmailAccount {
  id: string;
  address: string;
  domain: string;
  quotaMB: number;
  status: { phase: string; usedMB: number };
}

@Component({
  selector: 'app-emails',
  standalone: true,
  imports: [NgIf, NgFor, NgClass, FormsModule],
  template: `
    <div class="page-header">
      <h2>Email</h2>
      <a [href]="webmailUrl" target="_blank" style="display:inline-block;padding:6px 16px;background:#6366f1;color:#fff;border-radius:6px;text-decoration:none">Webmail</a>
    </div>

    <div *ngIf="error" class="alert error">{{ error }}</div>
    <div *ngIf="success" class="alert success">{{ success }}</div>

    <!-- ===== EMAIL DOMAINS ===== -->
    <div class="page-header" style="margin-top:16px">
      <h3 style="margin:0">Email Domains</h3>
      <button class="primary" (click)="showAddDomain = !showAddDomain">
        {{ showAddDomain ? 'Cancel' : 'Add Domain' }}
      </button>
    </div>

    <div *ngIf="showAddDomain" class="card">
      <h4>Add Email Domain</h4>
      <div class="form-group">
        <label>Domain</label>
        <input [(ngModel)]="domainForm.domain" placeholder="example.com">
      </div>
      <div class="form-group">
        <label><input type="checkbox" [(ngModel)]="domainForm.spamFilter"> Enable spam filter</label>
      </div>
      <button class="primary" (click)="createDomain()">Add Domain</button>
    </div>

    <div *ngIf="domains.length === 0 && !showAddDomain" class="card" style="color:#6b7280">
      No email domains yet. Add a domain first.
    </div>

    <!-- Domain list with nested accounts -->
    <div *ngFor="let d of domains" class="card">
      <div class="page-header">
        <div>
          <strong>{{ d.domain }}</strong>
          <span *ngIf="d.phase" class="badge" [ngClass]="d.phase === 'Active' ? 'green' : 'yellow'" style="margin-left:8px">{{ d.phase }}</span>
          <span style="margin-left:8px;color:#6b7280">{{ d.accountCount }} account(s)</span>
          <span *ngIf="d.spamFilter" style="margin-left:8px;color:#6b7280">spam filter on</span>
        </div>
        <div>
          <button (click)="toggleAccounts(d)">
            {{ expandedDomain === d.id ? 'Hide Accounts' : 'Show Accounts' }}
          </button>
          <button class="primary" (click)="startAddAccount(d)">Add Account</button>
          <button class="danger" (click)="deleteDomain(d)">Delete Domain</button>
        </div>
      </div>

      <!-- Add account form (inline, per domain) -->
      <div *ngIf="addingAccountToDomain === d.id" style="margin-top:12px;padding-top:12px;border-top:1px solid #e5e7eb">
        <h4 style="margin:0 0 8px">New account for {{ d.domain }}</h4>
        <div class="form-group">
          <label>Username (local part)</label>
          <div style="display:flex;gap:8px;align-items:center">
            <input [(ngModel)]="accountForm.localPart" placeholder="info" style="flex:1">
            <span style="color:#6b7280">{{ atSign }}{{ d.domain }}</span>
          </div>
        </div>
        <div class="form-group">
          <label>Quota (MB)</label>
          <input [(ngModel)]="accountForm.quotaMB" type="number" placeholder="1024">
        </div>
        <div class="form-group">
          <label>Password</label>
          <input [(ngModel)]="accountForm.password" type="password" placeholder="Min. 8 characters">
        </div>
        <button class="primary" (click)="createAccount(d)">Create Account</button>
        <button (click)="addingAccountToDomain = null" style="margin-left:8px">Cancel</button>
      </div>

      <!-- Account list for this domain -->
      <div *ngIf="expandedDomain === d.id" style="margin-top:12px">
        <div *ngIf="getAccounts(d.domain).length === 0" style="color:#6b7280;padding:8px 0">
          No accounts for this domain.
        </div>
        <table *ngIf="getAccounts(d.domain).length > 0">
          <thead>
            <tr><th>Address</th><th>Quota</th><th>Used</th><th>Status</th><th>Actions</th></tr>
          </thead>
          <tbody>
            <tr *ngFor="let a of getAccounts(d.domain)">
              <td>{{ a.address }}</td>
              <td>{{ a.quotaMB }} MB</td>
              <td>{{ a.status?.usedMB || 0 }} MB</td>
              <td><span class="badge" [ngClass]="a.status?.phase === 'Active' ? 'green' : 'yellow'">{{ a.status?.phase || 'Pending' }}</span></td>
              <td><button class="danger" (click)="deleteAccount(a)">Delete</button></td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>
  `
})
export class EmailsComponent implements OnInit {
  domains: EmailDomain[] = [];
  accountsByDomain: { [key: string]: EmailAccount[] } = {};
  expandedDomain: string | null = null;
  addingAccountToDomain: string | null = null;
  atSign = '@';

  showAddDomain = false;
  domainForm = { domain: '', spamFilter: false };
  accountForm = { localPart: '', quotaMB: 1024, password: '' };

  error = '';
  success = '';
  webmailUrl = /^\d+\.\d+\.\d+\.\d+$/.test(window.location.hostname)
    ? `http://${window.location.hostname}:30081`
    : `${window.location.protocol}//webmail.${window.location.hostname}`;
  constructor(private api: ApiService) {}

  ngOnInit(): void { this.loadDomains(); }

  loadDomains(): void {
    this.api.getEmailDomains().subscribe({
      next: (d: any[]) => this.domains = d,
      error: () => this.error = 'Failed to load email domains.'
    });
  }

  toggleAccounts(d: EmailDomain): void {
    if (this.expandedDomain === d.id) {
      this.expandedDomain = null;
      return;
    }
    this.expandedDomain = d.id;
    this.loadAccountsForDomain(d.domain);
  }

  loadAccountsForDomain(domain: string): void {
    this.api.getEmailAccounts().subscribe({
      next: (accounts: any[]) => {
        this.accountsByDomain[domain] = accounts.filter((a: any) => a.domain === domain);
      },
      error: () => this.error = 'Failed to load accounts.'
    });
  }

  getAccounts(domain: string): EmailAccount[] {
    return this.accountsByDomain[domain] || [];
  }

  createDomain(): void {
    this.error = ''; this.success = '';
    if (!this.domainForm.domain) { this.error = 'Domain is required.'; return; }
    this.api.createEmailDomain(this.domainForm).subscribe({
      next: () => {
        this.success = 'Email domain added.';
        this.showAddDomain = false;
        this.domainForm = { domain: '', spamFilter: false };
        this.loadDomains();
      },
      error: (e: any) => this.error = e.error?.error?.message || 'Failed to add domain.'
    });
  }

  deleteDomain(d: EmailDomain): void {
    if (!confirm(`Delete domain "${d.domain}" and all its accounts?`)) return;
    this.api.deleteEmailDomain(d.id).subscribe({
      next: () => { this.success = 'Domain deleted.'; this.loadDomains(); },
      error: (e: any) => this.error = e.error?.error?.message || 'Delete failed.'
    });
  }

  startAddAccount(d: EmailDomain): void {
    this.addingAccountToDomain = d.id;
    this.expandedDomain = d.id;
    this.accountForm = { localPart: '', quotaMB: 1024, password: '' };
    if (!this.accountsByDomain[d.domain]) {
      this.loadAccountsForDomain(d.domain);
    }
  }

  createAccount(d: EmailDomain): void {
    this.error = ''; this.success = '';
    if (!this.accountForm.localPart) { this.error = 'Username is required.'; return; }
    if (!this.accountForm.password || this.accountForm.password.length < 8) {
      this.error = 'Password must be at least 8 characters.'; return;
    }
    const address = `${this.accountForm.localPart}@${d.domain}`;
    this.api.createEmailAccount({ address, domain: d.domain, quotaMB: this.accountForm.quotaMB, password: this.accountForm.password } as any).subscribe({
      next: () => {
        this.success = `Account ${address} created.`;
        this.addingAccountToDomain = null;
        this.accountForm = { localPart: '', quotaMB: 1024, password: '' };
        this.loadAccountsForDomain(d.domain);
        this.loadDomains();
      },
      error: (e: any) => this.error = e.error?.error?.message || 'Failed to create account.'
    });
  }

  deleteAccount(a: EmailAccount): void {
    if (!confirm(`Delete account "${a.address}"?`)) return;
    this.api.deleteEmailAccount(a.id).subscribe({
      next: () => {
        this.success = 'Account deleted.';
        this.loadAccountsForDomain(a.domain);
        this.loadDomains();
      },
      error: (e: any) => this.error = e.error?.error?.message || 'Delete failed.'
    });
  }
}
