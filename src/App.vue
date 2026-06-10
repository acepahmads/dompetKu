<script setup>
import { ref, onMounted, nextTick, computed } from 'vue'
import Chart from 'chart.js/auto'

// Base State
const transactions = ref([])
const chatHistory = ref([])
const financials = ref({
  saldo: 0,
  total_tagihan: 0,
  tagihan_count: 0,
  sisa_aman: 0,
  total_pengeluaran: 0
})
const chartData = ref([])
const activeFilter = ref('all')
const typingMessage = ref('')
const isTyping = ref(false)
const showSendBtn = computed(() => typingMessage.value.trim().length > 0)
const chatViewport = ref(null)

// Voice Note State
const isRecording = ref(false)
const recordingSeconds = ref(0)
const recordingTimer = ref(null)

// Modals & Forms
const showReportModal = ref(false)
const reportTitle = ref('')
const reportContent = ref('')
const showManualModal = ref(false)

const formDesc = ref('')
const formType = ref('expense')
const formCategory = ref('makan')
const formNominal = ref('')
const formStatus = ref('planned')
const formDate = ref(new Date().toISOString().split('T')[0])

// Toast Notifications
const toasts = ref([])

// Categories config
const categories = ref({
  expense: [],
  income: []
})

// Chart.js reference
let myChart = null

// Fetch data from Go API
const fetchData = async () => {
  try {
    // 1. Fetch transactions
    let url = '/api/transactions'
    if (activeFilter.value !== 'all') {
      if (activeFilter.value === 'planned') {
        url += '?status=planned'
      } else {
        url += `?type=${activeFilter.value}`
      }
    }
    const txRes = await fetch(url)
    if (txRes.ok) {
      transactions.value = (await txRes.json()) || []
    }

    // 2. Fetch stats
    const finRes = await fetch('/api/financials')
    if (finRes.ok) {
      const data = await finRes.json()
      financials.value = {
        saldo: 0,
        total_tagihan: 0,
        tagihan_count: 0,
        sisa_aman: 0,
        total_pengeluaran: 0,
        ...(data.kpis || {})
      }
      chartData.value = data.chart || []
      renderChart()
    }

    // 3. Fetch chat history
    const chatRes = await fetch('/api/chat-history')
    if (chatRes.ok) {
      chatHistory.value = (await chatRes.json()) || []
      scrollToBottom()
    }

    // 4. Fetch planned keywords
    await fetchKeywords()

    // 5. Fetch categories
    await fetchCategories()

    // 6. Fetch recurring templates
    await fetchRecurringTemplates()
  } catch (err) {
    console.error('Error fetching data:', err)
    showToast('Koneksi ke backend gagal!', 'error')
  }
}

// Format currency
const formatRupiah = (val) => {
  return new Intl.NumberFormat('id-ID', {
    style: 'currency',
    currency: 'IDR',
    minimumFractionDigits: 0
  }).format(val).replace(/,\d+$/, '')
}

// Render chart using Chart.js
const renderChart = () => {
  const canvas = document.getElementById('expenseChartVue')
  if (!canvas) return

  const ctx = canvas.getContext('2d')
  
  if (chartData.value.length === 0) {
    if (myChart) {
      myChart.destroy()
      myChart = null
    }
    return
  }

  const labels = chartData.value.map(c => c.label)
  const values = chartData.value.map(c => c.value)
  
  const colors = [
    '#f43f5e', '#3b82f6', '#f59e0b', '#10b981', 
    '#8b5cf6', '#ec4899', '#14b8a6', '#f97316', '#64748b'
  ]

  if (myChart) {
    myChart.destroy()
  }

  myChart = new Chart(ctx, {
    type: 'doughnut',
    data: {
      labels,
      datasets: [{
        data: values,
        backgroundColor: colors.slice(0, labels.length),
        borderWidth: 1,
        borderColor: '#121826'
      }]
    },
    options: {
      responsive: true,
      maintainAspectRatio: false,
      plugins: {
        legend: {
          position: 'right',
          labels: {
            color: '#94a3b8',
            font: { family: 'Plus Jakarta Sans', size: 10 },
            boxWidth: 12
          }
        },
        tooltip: {
          callbacks: {
            label: (context) => `${context.label}: ${formatRupiah(context.raw)}`
          }
        }
      },
      cutout: '65%'
    }
  })
}

// Scroll chat to bottom
const scrollToBottom = () => {
  nextTick(() => {
    if (chatViewport.value) {
      chatViewport.value.scrollTop = chatViewport.value.scrollHeight
    }
  })
}

// Toast message handler
const showToast = (message, type = 'info') => {
  const id = Math.random().toString(36).substring(2, 9)
  toasts.value.push({ id, message, type })
  setTimeout(() => {
    toasts.value = toasts.value.filter(t => t.id !== id)
  }, 4000)
}

// Send chat message to Go backend
const sendMessage = async (overrideText = null) => {
  const text = overrideText || typingMessage.value.trim()
  if (!text) return

  if (!overrideText) {
    typingMessage.value = ''
  }

  // Optimistic User Msg append
  chatHistory.value.push({
    id: Date.now(),
    sender: 'user',
    message: text,
    created_at: new Date().toISOString()
  })
  scrollToBottom()

  isTyping.value = true

  try {
    const res = await fetch('/api/chat', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ message: text })
    })

    if (res.ok) {
      const data = await res.json()
      
      // Delay slightly for natural feel
      setTimeout(() => {
        isTyping.value = false
        chatHistory.value.push({
          id: Date.now() + 1,
          sender: 'bot',
          message: data.reply,
          created_at: new Date().toISOString()
        })
        fetchData()
        showToast('Catatan terproses!', 'success')
      }, 750)
    } else {
      isTyping.value = false
      showToast('Gagal memproses pesan.', 'error')
    }
  } catch (err) {
    isTyping.value = false
    console.error('Chat error:', err)
    showToast('Server backend offline!', 'error')
  }
}

const handleKeyDown = (e) => {
  if (e.key === 'Enter') {
    sendMessage()
  }
}

// Suggestion chip action
const applySuggestion = (text) => {
  sendMessage(text)
}

// Voice Note Recording Simulation
const toggleVoice = () => {
  if (isRecording.value) {
    // Finish
    clearInterval(recordingTimer.value)
    isRecording.value = false
    
    const voiceTranscripts = [
      'beli bakso beranak 30rb',
      'bayar listrik',
      'gaji sampingan 1.5jt',
      'saldo saya berapa?',
      'tagihan apa saja?',
      'bulan ini habis berapa?'
    ]
    const randomText = voiceTranscripts[Math.floor(Math.random() * voiceTranscripts.length)]
    showToast(`Voice Note terkirim! (Transkrip: "${randomText}")`, 'success')
    sendMessage(randomText)
  } else {
    // Start
    isRecording.value = true
    recordingSeconds.value = 0
    recordingTimer.value = setInterval(() => {
      recordingSeconds.value++
    }, 1000)
  }
}

const cancelVoice = () => {
  clearInterval(recordingTimer.value)
  isRecording.value = false
}

const formatVoiceTime = computed(() => {
  const mins = Math.floor(recordingSeconds.value / 60).toString().padStart(2, '0')
  const secs = (recordingSeconds.value % 60).toString().padStart(2, '0')
  return `${mins}:${secs}`
})

// Quick update planned to paid
const markAsPaid = async (id, desc) => {
  try {
    const res = await fetch(`/api/transactions/${id}/status`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ status: 'paid' })
    })

    if (res.ok) {
      showToast(`Tagihan "${desc}" berhasil dilunasi!`, 'success')
      sendMessage(`bayar ${desc.toLowerCase()}`) // feeds back to bot to log in chat
    } else {
      showToast('Gagal melunasi tagihan.', 'error')
    }
  } catch (err) {
    console.error(err)
  }
}

// Delete transaction
const deleteTx = async (id, desc) => {
  if (!confirm(`Hapus transaksi "${desc}"?`)) return
  try {
    const res = await fetch(`/api/transactions/${id}`, {
      method: 'DELETE'
    })
    if (res.ok) {
      showToast('Transaksi dihapus.', 'warning')
      fetchData()
    } else {
      showToast('Gagal menghapus transaksi.', 'error')
    }
  } catch (err) {
    console.error(err)
  }
}

// Reset database
const resetDB = async () => {
  if (!confirm('Apakah Anda yakin ingin me-reset database ke data demo bawaan? Semua data saat ini akan terhapus.')) return
  try {
    const res = await fetch('/api/transactions/reset', {
      method: 'POST'
    })
    if (res.ok) {
      showToast('Database berhasil di-reset!', 'success')
      fetchData()
    } else {
      showToast('Reset database gagal.', 'error')
    }
  } catch (err) {
    console.error(err)
  }
}

// Fetch Daily / Monthly plaintext report
const openReport = async (type) => {
  try {
    const res = await fetch(`/api/reports/${type}`)
    if (res.ok) {
      const data = await res.json()
      reportTitle.value = data.title
      reportContent.value = data.content
      showReportModal.value = true
    }
  } catch (err) {
    showToast('Gagal memuat laporan.', 'error')
  }
}

const copyReport = () => {
  navigator.clipboard.writeText(reportContent.value).then(() => {
    showToast('Teks laporan berhasil disalin!', 'success')
  }).catch(() => {
    showToast('Gagal menyalin laporan.', 'error')
  })
}

// Manual transaction submit
const submitManualTransaction = async () => {
  if (!formDesc.value || !formNominal.value) {
    showToast('Mohon lengkapi semua kolom wajib.', 'error')
    return
  }

  const payload = {
    tanggal: formDate.value,
    deskripsi: formDesc.value,
    kategori: formCategory.value,
    tipe: formType.value,
    nominal: parseFloat(formNominal.value),
    status: formStatus.value,
    due_date: formStatus.value === 'planned' ? new Date(Date.now() + 10*24*60*60*1000).toISOString().split('T')[0] : null
  }

  try {
    const res = await fetch('/api/transactions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    })

    if (res.ok) {
      showToast('Transaksi manual disimpan!', 'success')
      showManualModal.value = false
      fetchData()
      
      // Reset form
      formDesc.value = ''
      formNominal.value = ''
      formType.value = 'expense'
      formCategory.value = 'makan'
      formStatus.value = 'planned'
    } else {
      showToast('Gagal menyimpan transaksi.', 'error')
    }
  } catch (err) {
    showToast('Koneksi backend error.', 'error')
  }
}

// Form logic
const adjustFormCategories = () => {
  formCategory.value = formType.value === 'expense' ? 'makan' : 'gaji'
}

// Filter tab handler
const selectFilter = (filter) => {
  activeFilter.value = filter
  fetchData()
}

// Markdown formatter helper
const renderMessageText = (text) => {
  if (!text) return ''
  return String(text).replace(/\*\*(.*?)\*\*/g, '<strong>$1</strong>')
}

// Smart Alerts computed properties
const smartInsights = computed(() => {
  // Finds the highest category of successfully paid expense
  const monthlyExpenses = transactions.value.filter(t => t.tipe === 'expense' && t.status === 'paid')
  const catSums = {}
  monthlyExpenses.forEach(t => {
    catSums[t.kategori] = (catSums[t.kategori] || 0) + t.nominal
  })

  let topCategory = ''
  let topSum = 0
  for (const [cat, sum] of Object.entries(catSums)) {
    if (sum > topSum) {
      topSum = sum
      topCategory = cat
    }
  }

  if (topCategory) {
    const matchedLabel = categories.value.expense.find(c => c.id === topCategory)?.label || 'Lainnya'
    return `Pengeluaran terbesar bulan ini: <strong>${matchedLabel}</strong> (${formatRupiah(topSum)})`
  }
  return 'Belum ada catatan pengeluaran di bulan ini. 😊'
})

const smartReminder = computed(() => {
  const bills = transactions.value.filter(t => t.tipe === 'expense' && t.status === 'planned')
  if (bills.length > 0) {
    return `Tagihan <strong>${bills[0].deskripsi}</strong> sebesar <strong>${formatRupiah(bills[0].nominal)}</strong> mendekati jatuh tempo.`
  }
  return 'Semua tagihan lunas dan aman! 👍'
})

const smartWarning = computed(() => {
  const currentMonth = new Date().getMonth()
  const paidIncome = transactions.value.filter(t => t.tipe === 'income' && t.status === 'paid' && new Date(t.tanggal).getMonth() === currentMonth).reduce((sum, t) => sum + t.nominal, 0)
  const paidExpense = transactions.value.filter(t => t.tipe === 'expense' && t.status === 'paid' && new Date(t.tanggal).getMonth() === currentMonth).reduce((sum, t) => sum + t.nominal, 0)
  
  if (paidIncome > 0) {
    const pct = (paidExpense / paidIncome) * 100
    if (pct > 75) {
      return { text: `Bahaya! Pengeluaran bulanan mencapai <strong>${pct.toFixed(0)}%</strong> dari total pemasukan.`, level: 'danger' }
    } else if (pct > 50) {
      return { text: `Waspada! Pengeluaran bulanan memakan <strong>${pct.toFixed(0)}%</strong> dari total pemasukan.`, level: 'warning' }
    } else {
      return { text: `Bagus! Pengeluaran terkendali di kisaran <strong>${pct.toFixed(0)}%</strong> dari pemasukan.`, level: 'success' }
    }
  } else if (paidExpense > 0) {
    return { text: 'Belum ada pemasukan bulanan sedangkan pengeluaran terus berjalan!', level: 'danger' }
  }
  return { text: 'Pengeluaran masih dalam batas wajar.', level: 'success' }
})

// Format standard MySQL Dates to readable text
const formatTxDate = (sqlDate) => {
  const d = new Date(sqlDate)
  return d.toLocaleDateString('id-ID', { day: '2-digit', month: 'short', year: 'numeric' })
}

// Planned Keywords state & API integrations
const plannedKeywords = ref([])
const newKeywordInput = ref('')

// Recurring Templates state & API integrations
const recurringTemplates = ref([])

const fetchRecurringTemplates = async () => {
  try {
    const res = await fetch('/api/recurring-templates')
    if (res.ok) {
      recurringTemplates.value = (await res.json()) || []
    }
  } catch (err) {
    console.error('Error fetching recurring templates:', err)
  }
}

const deleteRecurringTemplate = async (id, desc) => {
  if (!confirm(`Hapus template "${desc}"?`)) return
  try {
    const res = await fetch(`/api/recurring-templates/${id}`, {
      method: 'DELETE'
    })
    if (res.ok) {
      showToast(`Template "${desc}" berhasil dihapus.`, 'warning')
      fetchRecurringTemplates()
    } else {
      showToast('Gagal menghapus template.', 'error')
    }
  } catch (err) {
    console.error(err)
    showToast('Koneksi backend error.', 'error')
  }
}

const fetchKeywords = async () => {
  try {
    const res = await fetch('/api/planned-keywords')
    if (res.ok) {
      plannedKeywords.value = await res.json()
    }
  } catch (err) {
    console.error('Error fetching planned keywords:', err)
  }
}

const fetchCategories = async () => {
  try {
    const res = await fetch('/api/categories')
    if (res.ok) {
      const list = await res.json()
      const grouped = {
        expense: [],
        income: []
      }
      list.forEach(c => {
        if (c.tipe === 'expense') {
          grouped.expense.push(c)
        } else if (c.tipe === 'income') {
          grouped.income.push(c)
        }
      })
      categories.value = grouped
    }
  } catch (err) {
    console.error('Error fetching categories:', err)
  }
}

const addKeyword = async () => {
  const kw = newKeywordInput.value.trim().toLowerCase()
  if (!kw) return

  try {
    const res = await fetch('/api/planned-keywords', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ keyword: kw })
    })

    if (res.ok) {
      newKeywordInput.value = ''
      showToast(`Kata kunci "${kw}" berhasil ditambahkan!`, 'success')
      fetchKeywords()
    } else if (res.status === 409) {
      showToast(`Kata kunci "${kw}" sudah terdaftar!`, 'warning')
    } else {
      showToast('Gagal menambahkan kata kunci.', 'error')
    }
  } catch (err) {
    console.error(err)
    showToast('Koneksi backend error.', 'error')
  }
}

const deleteKeyword = async (id) => {
  try {
    const res = await fetch(`/api/planned-keywords/${id}`, {
      method: 'DELETE'
    })
    if (res.ok) {
      showToast('Kata kunci dihapus.', 'warning')
      fetchKeywords()
    } else {
      showToast('Gagal menghapus kata kunci.', 'error')
    }
  } catch (err) {
    console.error(err)
    showToast('Koneksi backend error.', 'error')
  }
}

onMounted(() => {
  fetchData()
})
</script>

<template>
  <div class="grid grid-cols-1 lg:grid-cols-[380px_1fr] h-auto lg:h-screen w-screen bg-[#090d16] font-primary relative overflow-y-auto lg:overflow-hidden">
    
    <!-- LEFT PANEL: Simulated WhatsApp mobile frame -->
    <section class="bg-[#05080f] border-b lg:border-b-0 lg:border-r border-white/10 p-4 flex flex-col justify-center items-center h-auto min-h-[660px] lg:h-full shrink-0">
      <div class="w-full max-w-[340px] h-[680px] lg:h-full max-h-[calc(100vh-32px)] bg-[#0b141a] rounded-[20px] border-[3px] border-[#1e293b] shadow-2xl flex flex-col overflow-hidden relative">
        
        <!-- Status Bar -->
        <div class="bg-wa-dark-green px-4 py-1.5 flex justify-between items-center text-white/80 text-xs font-semibold">
          <span>22:02</span>
          <div class="flex items-center gap-1.5">
            <svg class="w-3.5 h-3.5" viewBox="0 0 24 24"><path fill="currentColor" d="M12 3c-4.97 0-9 4.03-9 9 0 2.12.74 4.07 1.97 5.61L4.35 19.4c-.39.39-.39 1.02 0 1.41.39.39 1.02.39 1.41 0l1.9-1.9C9.12 19.67 10.51 20 12 20c4.97 0 9-4.03 9-9s-4.03-9-9-9zm0 15c-3.31 0-6-2.69-6-6s2.69-6 6-6 6 2.69 6 6-2.69 6-6 6z"/></svg>
            <svg class="w-3.5 h-3.5" viewBox="0 0 24 24"><path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-6h2v6zm0-8h-2V7h2v2z"/></svg>
          </div>
        </div>

        <!-- WhatsApp Header -->
        <div class="bg-wa-dark-green px-3 py-2 flex items-center text-white gap-2 shadow-md">
          <div class="opacity-80 cursor-pointer hover:opacity-100 flex items-center">
            <svg class="w-6 h-6" viewBox="0 0 24 24"><path fill="currentColor" d="M20 11H7.83l5.59-5.59L12 4l-8 8 8 8 1.41-1.41L7.83 13H20v-2z"/></svg>
          </div>
          <div class="w-10 h-10 rounded-full bg-gradient-to-br from-emerald-500 to-emerald-700 flex justify-center items-center font-bold text-sm relative shadow shadow-black/30">
            DK
            <span class="absolute bottom-0 right-0 w-2.5 h-2.5 rounded-full bg-wa-green border-2 border-wa-dark-green"></span>
          </div>
          <div class="flex flex-col flex-grow leading-tight">
            <span class="font-semibold text-[14px]">DompetKu</span>
            <span class="text-[11px] opacity-80">online</span>
          </div>
          <div class="flex gap-3 text-white opacity-85">
            <svg class="w-5 h-5 cursor-pointer hover:scale-105 transition" viewBox="0 0 24 24"><path fill="currentColor" d="M17 10.5V7c0-.55-.45-1-1-1H4c-.55 0-1 .45-1 1v10c0 .55.45 1 1 1h12c.55 0 1-.45 1-1v-3.5l4 4v-11l-4 4z"/></svg>
            <svg class="w-5 h-5 cursor-pointer hover:scale-105 transition" viewBox="0 0 24 24"><path fill="currentColor" d="M20.01 15.38c-1.23 0-2.42-.2-3.53-.56a.977.977 0 0 0-1.01.24l-2.2 2.2a15.045 15.045 0 0 1-6.59-6.59l2.2-2.2c.28-.28.36-.67.25-1.02A11.36 11.36 0 0 1 8.5 4c0-.55-.45-1-1-1H4.03C3.47 3 3 3.47 3 4.02 3 13.4 10.6 21 19.98 21c.54 0 1-.46 1-1.02v-3.6c0-.55-.45-1-1-1z"/></svg>
          </div>
        </div>

        <!-- Chat Viewport -->
        <div ref="chatViewport" class="flex-1 min-h-0 overflow-y-auto px-2.5 py-4 flex flex-col gap-3 bg-[url('https://user-images.githubusercontent.com/15075759/28719144-86dc0f70-73b1-11e7-911d-60d70fcded21.png')] bg-blend-overlay bg-wa-chat-bg">
          <div class="flex justify-center my-2">
            <span class="bg-[#182229] text-[#8696a0] text-[10px] px-2 py-0.5 rounded shadow-sm">HARI INI</span>
          </div>

          <!-- Loop Chat Messages -->
          <div v-for="msg in chatHistory" :key="msg.id" class="flex w-full" :class="msg.sender === 'user' ? 'justify-end' : 'justify-start'">
            <div class="max-w-[85%] px-3 py-2 pb-5 rounded-lg text-xs leading-relaxed relative shadow shadow-black/20" 
                 :class="msg.sender === 'user' ? 'bg-wa-bubble-sent text-[#e9edef] rounded-tr-none' : 'bg-wa-bubble-received text-[#e9edef] rounded-tl-none'">
              <span v-html="renderMessageText(msg.message)"></span>
              <span class="absolute bottom-0.5 right-1.5 text-[9px] text-[#8696a0] flex items-center gap-0.5">
                22:02
                <svg v-if="msg.sender === 'user'" class="w-3.5 h-3.5 text-sky-400" viewBox="0 0 24 24"><path fill="currentColor" d="M18 7l-1.41-1.41-6.34 6.34 1.41 1.41L18 7zm4.24-1.41L11.66 16.17l-4.24-4.24-1.41 1.41 5.66 5.66L23.66 7l-1.42-1.41z"/></svg>
              </span>
            </div>
          </div>

          <!-- Typing Indicator -->
          <div v-if="isTyping" class="flex w-full justify-start">
            <div class="bg-wa-bubble-received p-3 rounded-lg text-xs max-w-[85%] shadow shadow-black/20 rounded-tl-none">
              <div class="flex gap-1 items-center h-2.5">
                <span class="w-1.5 h-1.5 rounded-full bg-[#8696a0] dot-animation-1"></span>
                <span class="w-1.5 h-1.5 rounded-full bg-[#8696a0] dot-animation-2"></span>
                <span class="w-1.5 h-1.5 rounded-full bg-[#8696a0] dot-animation-3"></span>
              </div>
            </div>
          </div>
        </div>

        <!-- Suggestion Chips -->
        <div class="bg-[#121b22] p-2 flex gap-1.5 overflow-x-auto whitespace-nowrap border-t border-white/5 scrollbar-thin">
          <button @click="applySuggestion('gaji 5 juta')" class="inline-block bg-[#202c33] text-wa-green border border-wa-green/20 text-[11px] px-2.5 py-1 rounded-full hover:bg-wa-green hover:text-[#0c151b] transition">gaji 5 juta</button>
          <button @click="applySuggestion('beli sayur 20rb')" class="inline-block bg-[#202c33] text-wa-green border border-wa-green/20 text-[11px] px-2.5 py-1 rounded-full hover:bg-wa-green hover:text-[#0c151b] transition">beli sayur 20rb</button>
          <button @click="applySuggestion('tagihan listrik 300rb')" class="inline-block bg-[#202c33] text-wa-green border border-wa-green/20 text-[11px] px-2.5 py-1 rounded-full hover:bg-wa-green hover:text-[#0c151b] transition">tagihan listrik 300rb</button>
          <button @click="applySuggestion('bayar listrik')" class="inline-block bg-[#202c33] text-wa-green border border-wa-green/20 text-[11px] px-2.5 py-1 rounded-full hover:bg-wa-green hover:text-[#0c151b] transition">bayar listrik</button>
          <button @click="applySuggestion('saldo saya berapa?')" class="inline-block bg-[#202c33] text-wa-green border border-wa-green/20 text-[11px] px-2.5 py-1 rounded-full hover:bg-wa-green hover:text-[#0c151b] transition">saldo saya berapa?</button>
          <button @click="applySuggestion('tagihan apa saja?')" class="inline-block bg-[#202c33] text-wa-green border border-wa-green/20 text-[11px] px-2.5 py-1 rounded-full hover:bg-wa-green hover:text-[#0c151b] transition">tagihan apa saja?</button>
        </div>

        <!-- Voice panel -->
        <div v-if="isRecording" class="bg-[#1f2c34] p-3.5 flex items-center gap-3 border-t border-white/5 animate-pulse">
          <div class="flex items-center gap-1.5 flex-grow">
            <span class="w-1 h-3 bg-wa-green rounded-full"></span>
            <span class="w-1 h-5 bg-wa-green rounded-full"></span>
            <span class="w-1 h-4 bg-wa-green rounded-full"></span>
            <span class="w-1 h-2 bg-wa-green rounded-full"></span>
            <span class="w-1 h-6 bg-wa-green rounded-full"></span>
          </div>
          <span class="text-xs font-mono text-white">{{ formatVoiceTime }}</span>
          <span class="text-[11px] text-[#8696a0]">Merekam...</span>
          <button @click="cancelVoice" class="text-orange-600 font-semibold text-xs hover:underline">Batal</button>
          <button @click="toggleVoice" class="bg-wa-green text-[#0c151b] font-semibold text-xs px-2.5 py-1 rounded">Selesai</button>
        </div>

        <!-- Text Input Panel -->
        <div v-else class="bg-[#1f2c34] p-1.5 flex items-center gap-2">
          <button class="text-[#8696a0] hover:text-white p-1">
            <svg class="w-6 h-6" viewBox="0 0 24 24"><path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 14H11v-2h2v2zm0-4H11V7h2v5z"/></svg>
          </button>
          <input type="text" v-model="typingMessage" @keydown="handleKeyDown" 
                 class="flex-grow bg-[#2a3942] rounded-lg text-white px-3 py-1.5 text-xs outline-none placeholder-[#8696a0]" placeholder="Ketik pesan..." />
          <button class="text-[#8696a0] hover:text-white p-1">
            <svg class="w-6 h-6" viewBox="0 0 24 24"><path fill="currentColor" d="M16.5 6v11.5c0 2.21-1.79 4-4 4s-4-1.79-4-4V5c0-3.31 2.69-6 6-6s6 2.69 6 6v8.5c0 1.38-1.12 2.5-2.5 2.5S15 14.88 15 13.5V6h-2v7.5c0 .28.22.5.5.5s.5-.22.5-.5V5c0-2.21-1.79-4-4-4s-4 1.79-4 4v12.5c0 1.1.9 2 2 2s2-.9 2-2V6h2z"/></svg>
          </button>
          
          <!-- Mic / Send buttons -->
          <button v-if="showSendBtn" @click="sendMessage()" class="bg-[#00a884] text-white w-9 h-9 rounded-full flex justify-center items-center hover:scale-105 transition flex-shrink-0">
            <svg class="w-5 h-5 ml-0.5" viewBox="0 0 24 24"><path fill="currentColor" d="M2.01 21L23 12 2.01 3 2 10l15 2-15 2z"/></svg>
          </button>
          <button v-else @click="toggleVoice" class="bg-[#00a884] text-white w-9 h-9 rounded-full flex justify-center items-center hover:scale-105 transition flex-shrink-0">
            <svg class="w-5 h-5" viewBox="0 0 24 24"><path fill="currentColor" d="M12 14c1.66 0 3-1.34 3-3V5c0-1.66-1.34-3-3-3S9 3.34 9 5v6c0 1.66 1.34 3 3 3zm5.3-3c0 3-2.54 5.1-5.3 5.1S6.7 14 6.7 11H5c0 3.41 2.72 6.23 6 6.72V21h2v-3.28c3.28-.48 6-3.3 6-6.72h-1.7z"/></svg>
          </button>
        </div>
      </div>
    </section>
 
    <!-- RIGHT PANEL: Executive Real-time Dashboard -->
    <main class="h-auto lg:h-screen overflow-y-auto p-6 flex flex-col gap-6 flex-grow">
      
      <!-- Top Header -->
      <header class="flex flex-col sm:flex-row justify-between items-start sm:items-center border-b border-white/10 pb-4">
        <div class="flex items-center gap-3">
          <div class="bg-gradient-to-br from-wa-green to-[#00a884] w-11 h-11 rounded-xl flex justify-center items-center font-playpen font-bold text-lg text-[#0c151b] shadow-inner shadow-black/10">
            DK
          </div>
          <div>
            <h1 class="text-xl font-bold bg-gradient-to-r from-white to-slate-400 bg-clip-text text-transparent">DompetKu</h1>
            <p class="text-xs text-text-secondary">Dashboard Keuangan Keluarga WhatsApp Real-time</p>
          </div>
        </div>
        <div class="flex gap-2.5 mt-3 sm:mt-0">
          <button @click="resetDB" class="inline-flex items-center gap-1.5 px-3 py-1.5 bg-bg-surface-elevated text-text-primary border border-border-custom rounded hover:bg-white/10 text-xs font-semibold transition">
            <svg class="w-3.5 h-3.5" viewBox="0 0 24 24"><path fill="currentColor" d="M17.65 6.35A7.958 7.958 0 0 0 12 4c-4.42 0-7.99 3.58-7.99 8s3.57 8 7.99 8c3.73 0 6.84-2.55 7.73-6h-2.08A5.99 5.99 0 0 1 12 18c-3.31 0-6-2.69-6-6s2.69-6 6-6c1.66 0 3.14.69 4.22 1.78L13 11h7V4l-2.35 2.35z"/></svg>
            Reset & Demo Data
          </button>
          <button @click="showManualModal = true" class="inline-flex items-center gap-1.5 px-3 py-1.5 bg-gradient-to-r from-emerald-500 to-emerald-600 text-white rounded hover:shadow-lg hover:shadow-emerald-500/20 text-xs font-semibold transition">
            <svg class="w-3.5 h-3.5" viewBox="0 0 24 24"><path fill="currentColor" d="M19 13h-6v6h-2v-6H5v-2h6V5h2v6h6v2z"/></svg>
            Transaksi Baru
          </button>
        </div>
      </header>

      <!-- KPI metrics board -->
      <section class="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-4 gap-4">
        <!-- Saldo card -->
        <div class="bg-bg-surface/40 border border-border-custom rounded-2xl p-5 flex items-center gap-4 backdrop-blur hover:translate-y-[-2px] hover:border-white/20 transition-all duration-300">
          <div class="w-12 h-12 rounded-xl bg-income/10 text-income flex justify-center items-center">
            <svg class="w-6 h-6" viewBox="0 0 24 24"><path fill="currentColor" d="M21 18v1c0 1.1-.9 2-2 2H5c-1.11 0-2-.9-2-2V5c0-1.1.89-2 2-2h14c1.1 0 2 .9 2 2v1h-9c-1.11 0-2 .9-2 2v8c0 1.1.89 2 2 2h9zm-9-2h10V8H12v8zm4-2.5c-.83 0-1.5-.67-1.5-1.5s.67-1.5 1.5-1.5 1.5.67 1.5 1.5-.67 1.5-1.5 1.5z"/></svg>
          </div>
          <div>
            <span class="text-[10px] text-text-secondary uppercase tracking-wider">Saldo Saat Ini (Sukses)</span>
            <h2 class="text-xl font-bold text-income mt-0.5">{{ formatRupiah(financials.saldo) }}</h2>
            <span class="text-[10px] text-text-muted">Pemasukan - pengeluaran lunas</span>
          </div>
        </div>

        <!-- Total Pengeluaran (Lunas) Card -->
        <div class="bg-bg-surface/40 border border-border-custom rounded-2xl p-5 flex items-center gap-4 backdrop-blur hover:translate-y-[-2px] hover:border-white/20 transition-all duration-300">
          <div class="w-12 h-12 rounded-xl bg-expense/10 text-expense flex justify-center items-center">
            <svg class="w-6 h-6" viewBox="0 0 24 24"><path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 10V7h-2v5H8l4 4 4-4h-3z"/></svg>
          </div>
          <div>
            <span class="text-[10px] text-text-secondary uppercase tracking-wider">Total Pengeluaran (Lunas)</span>
            <h2 class="text-xl font-bold text-expense mt-0.5">{{ formatRupiah(financials.total_pengeluaran || 0) }}</h2>
            <span class="text-[10px] text-text-muted">Total pengeluaran sukses dibayar</span>
          </div>
        </div>

        <!-- Tagihan Card -->
        <div class="bg-bg-surface/40 border border-border-custom rounded-2xl p-5 flex items-center gap-4 backdrop-blur hover:translate-y-[-2px] hover:border-white/20 transition-all duration-300">
          <div class="w-12 h-12 rounded-xl bg-planned/10 text-planned flex justify-center items-center">
            <svg class="w-6 h-6" viewBox="0 0 24 24"><path fill="currentColor" d="M19 3H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm-5 14H7v-2h7v2zm3-4H7v-2h10v2zm0-4H7V7h10v2z"/></svg>
          </div>
          <div>
            <span class="text-[10px] text-text-secondary uppercase tracking-wider">Total Tagihan (Planned)</span>
            <h2 class="text-xl font-bold text-planned mt-0.5">{{ formatRupiah(financials.total_tagihan) }}</h2>
            <span class="text-[10px] text-text-muted">{{ financials.tagihan_count }} Tagihan belum dibayar</span>
          </div>
        </div>

        <!-- Sisa Aman Card -->
        <div class="bg-bg-surface/40 border border-border-custom rounded-2xl p-5 flex items-center gap-4 backdrop-blur hover:translate-y-[-2px] hover:border-white/20 transition-all duration-300">
          <div class="w-12 h-12 rounded-xl bg-info-blue/10 text-info-blue flex justify-center items-center">
            <svg class="w-6 h-6" viewBox="0 0 24 24"><path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/></svg>
          </div>
          <div>
            <span class="text-[10px] text-text-secondary uppercase tracking-wider">Sisa Saldo Aman</span>
            <h2 class="text-xl font-bold mt-0.5" :class="financials.sisa_aman < 0 ? 'text-expense' : 'text-info-blue'">
              {{ formatRupiah(financials.sisa_aman) }}
            </h2>
            <span class="text-[10px]" :class="financials.sisa_aman < 0 ? 'text-expense/80' : 'text-text-muted'">
              {{ financials.sisa_aman < 0 ? 'Perhatian! Tagihan melebihi saldo.' : 'Keuangan aman terkendali' }}
            </span>
          </div>
        </div>
      </section>

      <!-- Main Layout Details -->
      <div class="grid grid-cols-1 xl:grid-cols-[1fr_340px] gap-5 items-start">
        
        <!-- Transaction Table Block -->
        <div class="bg-bg-surface border border-border-custom rounded-2xl overflow-hidden flex flex-col">
          <div class="px-5 py-4 border-b border-border-custom flex flex-col sm:flex-row justify-between items-start sm:items-center gap-3">
            <h3 class="font-semibold text-white text-[15px]">Daftar Transaksi</h3>
            
            <!-- Filters -->
            <div class="flex gap-1 bg-black/20 p-0.5 border border-white/5 rounded">
              <button @click="selectFilter('all')" :class="activeFilter === 'all' ? 'bg-bg-surface-elevated text-white' : 'text-text-secondary'" class="text-[10px] font-semibold px-2.5 py-1 rounded transition hover:text-white">Semua</button>
              <button @click="selectFilter('income')" :class="activeFilter === 'income' ? 'bg-bg-surface-elevated text-white' : 'text-text-secondary'" class="text-[10px] font-semibold px-2.5 py-1 rounded transition hover:text-white">Pemasukan</button>
              <button @click="selectFilter('expense')" :class="activeFilter === 'expense' ? 'bg-bg-surface-elevated text-white' : 'text-text-secondary'" class="text-[10px] font-semibold px-2.5 py-1 rounded transition hover:text-white">Pengeluaran</button>
              <button @click="selectFilter('planned')" :class="activeFilter === 'planned' ? 'bg-bg-surface-elevated text-white' : 'text-text-secondary'" class="text-[10px] font-semibold px-2.5 py-1 rounded transition hover:text-white">Tagihan</button>
            </div>
          </div>
          
          <div class="overflow-x-auto max-h-[440px] scrollbar-thin">
            <table class="w-full text-left border-collapse text-xs">
              <thead>
                <tr class="bg-white/[0.01] text-text-secondary border-b border-white/10 uppercase text-[9px] tracking-wider">
                  <th class="px-4 py-3 font-semibold">Tanggal</th>
                  <th class="px-4 py-3 font-semibold">Deskripsi</th>
                  <th class="px-4 py-3 font-semibold">Kategori</th>
                  <th class="px-4 py-3 font-semibold">Tipe</th>
                  <th class="px-4 py-3 font-semibold">Nominal</th>
                  <th class="px-4 py-3 font-semibold">Status</th>
                  <th class="px-4 py-3 font-semibold text-center">Aksi</th>
                </tr>
              </thead>
              <tbody>
                <tr v-if="transactions.length === 0">
                  <td colspan="7" class="text-center text-text-muted py-12">Tidak ada transaksi ditemukan</td>
                </tr>
                <tr v-for="t in transactions" :key="t.id" class="border-b border-white/5 hover:bg-white/[0.02] transition duration-200">
                  <td class="px-4 py-3 text-text-secondary">{{ formatTxDate(t.tanggal) }}</td>
                  <td class="px-4 py-3 font-semibold text-white">{{ t.deskripsi }}</td>
                  <td class="px-4 py-3 capitalize text-text-secondary">{{ t.kategori }}</td>
                  <td class="px-4 py-3">
                    <span :class="t.tipe === 'income' ? 'bg-info-blue/10 text-info-blue border-info-blue/20' : 'bg-expense/10 text-expense border-expense/20'" class="px-2 py-0.5 rounded text-[10px] border font-semibold">
                      {{ t.tipe === 'income' ? 'Pemasukan' : 'Pengeluaran' }}
                    </span>
                  </td>
                  <td class="px-4 py-3 font-bold" :class="t.tipe === 'income' ? 'text-income' : 'text-white'">
                    {{ t.tipe === 'income' ? '+' : '-' }} {{ formatRupiah(t.nominal) }}
                  </td>
                  <td class="px-4 py-3">
                    <span :class="t.status === 'paid' ? 'bg-income/10 text-income' : 'bg-planned/10 text-planned'" class="px-2 py-0.5 rounded-full text-[9px] font-bold uppercase tracking-wider">
                      {{ t.status === 'paid' ? 'Lunas' : 'Tagihan' }}
                    </span>
                  </td>
                  <td class="px-4 py-3">
                    <div class="flex items-center justify-center gap-1.5">
                      <button v-if="t.status === 'planned'" @click="markAsPaid(t.id, t.deskripsi)" title="Tandai Lunas" class="p-1 hover:bg-white/10 rounded text-text-secondary hover:text-income transition">
                        <svg class="w-4 h-4" viewBox="0 0 24 24"><path fill="currentColor" d="M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z"/></svg>
                      </button>
                      <button @click="deleteTx(t.id, t.deskripsi)" title="Hapus" class="p-1 hover:bg-white/10 rounded text-text-secondary hover:text-expense transition">
                        <svg class="w-4 h-4" viewBox="0 0 24 24"><path fill="currentColor" d="M6 19c0 1.1.9 2 2 2h8c1.1 0 2-.9 2-2V7H6v12zM19 4h-3.5l-1-1h-5l-1 1H5v2h14V4z"/></svg>
                      </button>
                    </div>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>

        <!-- Right Side Widgets Stack -->
        <div class="flex flex-col gap-5">
          
          <!-- Category distribution doughnut chart -->
          <div class="bg-bg-surface border border-border-custom rounded-2xl p-4 flex flex-col">
            <h3 class="font-semibold text-white text-xs border-b border-white/10 pb-2 mb-3">Distribusi Pengeluaran</h3>
            <div class="relative h-[180px] w-full flex items-center justify-center">
              <canvas v-show="chartData.length > 0" id="expenseChartVue"></canvas>
              <div v-if="chartData.length === 0" class="text-text-muted text-[11px] text-center p-6">
                Belum ada data pengeluaran terbayar untuk bulan ini.
              </div>
            </div>
          </div>

          <!-- Smart Alerts & Warnings -->
          <div class="bg-bg-surface border border-border-custom rounded-2xl p-4 flex flex-col gap-3.5">
            <h3 class="font-semibold text-white text-xs border-b border-white/10 pb-2">Smart Features & Alerts</h3>
            <div class="flex flex-col gap-2.5">
              
              <!-- Category Insight -->
              <div class="bg-income/4 border border-income/15 p-2.5 rounded-lg text-[11px] flex flex-col gap-1">
                <span class="bg-income/15 text-income font-extrabold text-[9px] px-1.5 py-0.5 rounded w-max tracking-wider">INSIGHT</span>
                <span class="text-slate-200" v-html="smartInsights"></span>
              </div>
              
              <!-- Planned Reminder -->
              <div class="bg-planned/4 border border-planned/15 p-2.5 rounded-lg text-[11px] flex flex-col gap-1">
                <span class="bg-planned/15 text-planned font-extrabold text-[9px] px-1.5 py-0.5 rounded w-max tracking-wider">REMINDER</span>
                <span class="text-slate-200" v-html="smartReminder"></span>
              </div>
              
              <!-- Warnings -->
              <div :class="smartWarning.level === 'danger' ? 'bg-expense/4 border-expense/15' : smartWarning.level === 'warning' ? 'bg-planned/4 border-planned/15' : 'bg-income/4 border-income/15'"
                   class="p-2.5 rounded-lg text-[11px] flex flex-col gap-1 border">
                <span :class="smartWarning.level === 'danger' ? 'bg-expense/15 text-expense' : smartWarning.level === 'warning' ? 'bg-planned/15 text-planned' : 'bg-income/15 text-income'"
                      class="font-extrabold text-[9px] px-1.5 py-0.5 rounded w-max tracking-wider">WARNING</span>
                <span class="text-slate-200" v-html="smartWarning.text"></span>
              </div>

            </div>
          </div>

          <!-- Financial Report Generation Panel -->
          <div class="bg-bg-surface border border-border-custom rounded-2xl p-4 flex flex-col">
            <h3 class="font-semibold text-white text-xs border-b border-white/10 pb-2 mb-2">Laporan Keuangan</h3>
            <p class="text-[10px] text-text-secondary leading-relaxed mb-3.5">
              Generate teks laporan harian atau bulanan kamu untuk disalin dan dibagikan langsung ke grup WhatsApp keluarga.
            </p>
            <div class="flex gap-2">
              <button @click="openReport('harian')" class="flex-1 inline-flex justify-center items-center gap-1.5 py-2 px-3 bg-bg-surface-elevated hover:bg-white/10 border border-border-custom text-text-primary rounded text-xs font-semibold transition">
                <svg class="w-3.5 h-3.5 text-text-secondary" viewBox="0 0 24 24"><path fill="currentColor" d="M19 3h-1V1h-2v2H8V1H6v2H5c-1.11 0-1.99.9-1.99 2L3 19c0 1.1.89 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H5V8h14v11zM7 10h5v5H7z"/></svg>
                Laporan Harian
              </button>
              <button @click="openReport('bulanan')" class="flex-1 inline-flex justify-center items-center gap-1.5 py-2 px-3 bg-bg-surface-elevated hover:bg-white/10 border border-border-custom text-text-primary rounded text-xs font-semibold transition">
                <svg class="w-3.5 h-3.5 text-text-secondary" viewBox="0 0 24 24"><path fill="currentColor" d="M19 3h-1V1h-2v2H8V1H6v2H5c-1.1 0-2 .9-2 2v14c0 1.1.9 2 2 2h14c1.1 0 2-.9 2-2V5c0-1.1-.9-2-2-2zm0 16H5V9h14v10zm-4-7h-2v-2h2v2zm0 4h-2v-2h2v2zm-4-4H9v-2h2v2zm0 4H9v-2h2v2z"/></svg>
                Laporan Bulanan
              </button>
            </div>
          </div>

          <!-- Template Transaksi Bulanan Panel -->
          <div class="bg-bg-surface border border-border-custom rounded-2xl p-4 flex flex-col gap-3">
            <h3 class="font-semibold text-white text-xs border-b border-white/10 pb-2 flex justify-between items-center">
              <span>Template Transaksi Bulanan</span>
              <span class="text-[9px] bg-bg-surface-elevated text-info-blue px-1.5 py-0.5 rounded font-extrabold uppercase tracking-wider">Rutin</span>
            </h3>
            <p class="text-[10px] text-text-secondary leading-relaxed">
              Daftar transaksi rutin bulanan yang otomatis dibuat setiap awal siklus gajian. Tambahkan baru lewat chat: <code class="text-wa-green font-mono bg-black/45 px-1 py-0.5 rounded">tambah template: - gaji 5jt tiap 25</code>
            </p>
            
            <!-- Templates List -->
            <div class="flex flex-col gap-1.5 max-h-[160px] overflow-y-auto pr-1 scrollbar-thin">
              <div v-for="tmpl in recurringTemplates" :key="tmpl.id"
                   class="flex items-center justify-between bg-bg-surface-elevated p-2 rounded-lg border border-white/5 hover:border-info-blue/30 transition">
                <div class="flex flex-col min-w-0">
                  <span class="text-[11px] font-semibold text-white truncate">{{ tmpl.deskripsi }}</span>
                  <span class="text-[9px] text-text-muted">
                    {{ tmpl.tipe === 'income' ? 'Pemasukan' : 'Pengeluaran' }} &bull; tiap tgl {{ tmpl.target_day }}
                  </span>
                </div>
                <div class="flex items-center gap-2 flex-shrink-0">
                  <span :class="tmpl.tipe === 'income' ? 'text-income' : 'text-slate-300'" class="text-[10px] font-bold">
                    {{ formatRupiah(tmpl.nominal) }}
                  </span>
                  <button @click="deleteRecurringTemplate(tmpl.id, tmpl.deskripsi)" title="Hapus Template"
                          class="text-text-muted hover:text-expense text-sm p-1 leading-none transition">
                    &times;
                  </button>
                </div>
              </div>
              <div v-if="recurringTemplates.length === 0" class="text-[10px] text-text-muted italic text-center py-2">
                Belum ada template terdaftar.
              </div>
            </div>
          </div>

          <!-- Planned Keywords Settings Panel -->
          <div class="bg-bg-surface border border-border-custom rounded-2xl p-4 flex flex-col gap-3">
            <h3 class="font-semibold text-white text-xs border-b border-white/10 pb-2 flex justify-between items-center">
              <span>Kata Kunci Tagihan</span>
              <span class="text-[9px] bg-bg-surface-elevated text-planned px-1.5 py-0.5 rounded font-extrabold uppercase tracking-wider">Settings</span>
            </h3>
            <p class="text-[10px] text-text-secondary leading-relaxed">
              Atur kata kunci untuk mengarahkan transaksi menjadi <strong>Tagihan (Planned)</strong> secara otomatis. Sistem juga mendukung toleransi salah ketik (fuzzy).
            </p>
            
            <!-- Keywords Chips List -->
            <div class="flex flex-wrap gap-1.5 max-h-[120px] overflow-y-auto pr-1 scrollbar-thin">
              <span v-for="kw in plannedKeywords" :key="kw.id" 
                    class="inline-flex items-center gap-1 bg-bg-surface-elevated text-text-primary text-[10px] px-2.5 py-1 rounded-full border border-white/5 hover:border-planned/30 transition">
                {{ kw.keyword }}
                <button @click="deleteKeyword(kw.id)" class="text-text-muted hover:text-expense text-[11px] leading-none ml-0.5 font-bold transition">&times;</button>
              </span>
              <span v-if="plannedKeywords.length === 0" class="text-[10px] text-text-muted italic">Tidak ada kata kunci terdaftar. Default: tagihan, rencana, planned...</span>
            </div>

            <!-- Add Keyword Form -->
            <form @submit.prevent="addKeyword" class="flex gap-2 mt-1">
              <input type="text" v-model="newKeywordInput" placeholder="Tambah kata kunci (cth: tempo)" required
                     class="flex-grow bg-bg-surface-elevated border border-border-custom rounded text-white px-2.5 py-1 text-[11px] outline-none focus:border-planned/45 transition placeholder-text-muted" />
              <button type="submit" class="bg-planned hover:bg-orange-500 text-black font-semibold text-[10px] px-3 py-1 rounded transition">
                Tambah
              </button>
            </form>
          </div>

        </div>
      </div>
    </main>

    <!-- MODAL 1: Financial Plaintext Report Display -->
    <div v-if="showReportModal" class="fixed inset-0 bg-black/85 backdrop-blur-sm flex justify-center items-center z-[1000] p-4">
      <div class="bg-bg-surface border border-border-custom rounded-2xl w-full max-w-[480px] shadow-2xl overflow-hidden scale-100 transition-transform">
        <div class="px-5 py-4 border-b border-border-custom flex justify-between items-center bg-white/[0.01]">
          <h3 class="font-bold text-white text-sm">{{ reportTitle }}</h3>
          <button @click="showReportModal = false" class="text-text-secondary hover:text-white text-xl leading-none">&times;</button>
        </div>
        <div class="p-5">
          <div class="bg-wa-chat-bg border border-border-custom rounded-lg p-3 max-h-[280px] overflow-y-auto font-mono text-xs text-[#e9edef] whitespace-pre-wrap leading-relaxed shadow-inner">
            {{ reportContent }}
          </div>
        </div>
        <div class="px-5 py-3.5 border-t border-border-custom flex justify-end gap-2 bg-white/[0.01]">
          <button @click="copyReport" class="px-3.5 py-1.5 bg-bg-surface-elevated hover:bg-white/10 text-text-primary text-xs font-semibold rounded border border-border-custom transition">
            Salin Laporan
          </button>
          <button @click="showReportModal = false" class="px-3.5 py-1.5 bg-gradient-to-r from-emerald-500 to-emerald-600 text-white text-xs font-semibold rounded hover:shadow transition">
            Tutup
          </button>
        </div>
      </div>
    </div>

    <!-- MODAL 2: Create Manual Transaction Fallback Form -->
    <div v-if="showManualModal" class="fixed inset-0 bg-black/85 backdrop-blur-sm flex justify-center items-center z-[1000] p-4">
      <div class="bg-bg-surface border border-border-custom rounded-2xl w-full max-w-[480px] shadow-2xl overflow-hidden">
        <div class="px-5 py-4 border-b border-border-custom flex justify-between items-center bg-white/[0.01]">
          <h3 class="font-bold text-white text-sm">Tambah Transaksi Manual</h3>
          <button @click="showManualModal = false" class="text-text-secondary hover:text-white text-xl leading-none">&times;</button>
        </div>
        <form @submit.prevent="submitManualTransaction" class="p-5 flex flex-col gap-3 text-xs">
          
          <div class="flex flex-col gap-1">
            <label class="font-bold text-[10px] text-text-secondary uppercase tracking-wider">Deskripsi</label>
            <input type="text" v-model="formDesc" placeholder="Contoh: Beli beras kemasan" required
                   class="bg-bg-surface-elevated border border-border-custom text-white px-3 py-2 rounded outline-none focus:border-wa-green transition" />
          </div>

          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <label class="font-bold text-[10px] text-text-secondary uppercase tracking-wider">Tipe</label>
              <select v-model="formType" @change="adjustFormCategories" required
                      class="bg-bg-surface-elevated border border-border-custom text-white px-3 py-2 rounded outline-none focus:border-wa-green transition">
                <option value="expense">Pengeluaran (Expense)</option>
                <option value="income">Pemasukan (Income)</option>
              </select>
            </div>
            <div class="flex flex-col gap-1">
              <label class="font-bold text-[10px] text-text-secondary uppercase tracking-wider">Kategori</label>
              <select v-model="formCategory" required
                      class="bg-bg-surface-elevated border border-border-custom text-white px-3 py-2 rounded outline-none focus:border-wa-green transition">
                <option v-for="cat in categories[formType]" :key="cat.id" :value="cat.id">
                  {{ cat.label }}
                </option>
              </select>
            </div>
          </div>

          <div class="grid grid-cols-2 gap-3">
            <div class="flex flex-col gap-1">
              <label class="font-bold text-[10px] text-text-secondary uppercase tracking-wider">Nominal (Rp)</label>
              <input type="number" v-model="formNominal" placeholder="Contoh: 150000" min="1" required
                     class="bg-bg-surface-elevated border border-border-custom text-white px-3 py-2 rounded outline-none focus:border-wa-green transition" />
            </div>
            <div class="flex flex-col gap-1">
              <label class="font-bold text-[10px] text-text-secondary uppercase tracking-wider">Status</label>
              <select v-model="formStatus" required
                      class="bg-bg-surface-elevated border border-border-custom text-white px-3 py-2 rounded outline-none focus:border-wa-green transition">
                <option value="paid">Lunas (Paid)</option>
                <option value="planned">Direncanakan (Planned)</option>
              </select>
            </div>
          </div>

          <div class="flex flex-col gap-1">
            <label class="font-bold text-[10px] text-text-secondary uppercase tracking-wider">Tanggal</label>
            <input type="date" v-model="formDate" required
                   class="bg-bg-surface-elevated border border-border-custom text-white px-3 py-2 rounded outline-none focus:border-wa-green transition" />
          </div>

          <div class="flex justify-end gap-2 border-t border-white/5 pt-4 mt-2">
            <button type="button" @click="showManualModal = false" class="px-3.5 py-1.5 bg-bg-surface-elevated hover:bg-white/10 text-text-primary font-semibold rounded border border-border-custom transition">
              Batal
            </button>
            <button type="submit" class="px-3.5 py-1.5 bg-gradient-to-r from-emerald-500 to-emerald-600 text-white font-semibold rounded hover:shadow transition">
              Simpan Transaksi
            </button>
          </div>
        </form>
      </div>
    </div>

    <!-- Toast Alert Notifications Stack -->
    <div class="fixed bottom-6 right-6 flex flex-col gap-2 z-[1100]">
      <div v-for="toast in toasts" :key="toast.id" 
           :class="toast.type === 'success' ? 'border-l-income' : toast.type === 'warning' ? 'border-l-planned' : 'border-l-info-blue'"
           class="bg-[#1e2640] border-l-4 text-white text-xs px-4 py-3 rounded shadow-lg flex items-center gap-3 min-w-[240px] animate-[slideIn_0.3s_cubic-bezier(0.16,1,0.3,1)_forwards]">
        <svg v-if="toast.type === 'success'" class="w-4 h-4 text-income" viewBox="0 0 24 24"><path fill="currentColor" d="M9 16.17L4.83 12l-1.42 1.41L9 19 21 7l-1.41-1.41z"/></svg>
        <svg v-else class="w-4 h-4 text-planned" viewBox="0 0 24 24"><path fill="currentColor" d="M12 2C6.48 2 2 6.48 2 12s4.48 10 10 10 10-4.48 10-10S17.52 2 12 2zm1 15h-2v-2h2v2zm0-4h-2V7h2v6z"/></svg>
        <span>{{ toast.message }}</span>
      </div>
    </div>

  </div>
</template>

<style>
@keyframes slideIn {
  from { transform: translateX(50px); opacity: 0; }
  to { transform: translateX(0); opacity: 1; }
}
</style>
