import React from 'react'
import ReactDOM from 'react-dom/client'
import { App as AntApp, ConfigProvider } from 'antd'
import zhCN from 'antd/locale/zh_CN'
import { initBackend } from './api'
import App from './App'
import './style.css'

initBackend().catch((e) => { document.getElementById('root')!.textContent = String(e); throw e }).then(() => {
  ReactDOM.createRoot(document.getElementById('root')!).render(
    <React.StrictMode>
      <ConfigProvider locale={zhCN} button={{ autoInsertSpace: false }} theme={{ token: { colorPrimary: '#ff6600', borderRadius: 6, fontSize: 14 } }}>
        <AntApp><App /></AntApp>
      </ConfigProvider>
    </React.StrictMode>,
  )
})
